[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Task,
    [Parameter(Mandatory = $true)][string]$Store,
    [Parameter(Mandatory = $true)][string]$WorkingDirectory
)

$ErrorActionPreference = 'Stop'
$source = @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;

public static class NativeTuiConPty {
    private const uint ExtendedStartupInfoPresent = 0x00080000;
    private const uint StartfUseStdHandles = 0x00000100;
    private const int PseudoConsoleAttribute = 0x00020016;
    private const int MaxFrameBytes = 65536;
    private const int MaxDrainBytes = 262144;

    [StructLayout(LayoutKind.Sequential)]
    private struct COORD {
        public short X;
        public short Y;
        public COORD(short x, short y) { X = x; Y = y; }
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct STARTUPINFO {
        public uint cb;
        public IntPtr reserved;
        public IntPtr desktop;
        public IntPtr title;
        public uint x, y, xSize, ySize, xCountChars, yCountChars, fillAttribute, flags;
        public ushort showWindow, reserved2;
        public IntPtr reserved3;
        public IntPtr stdin, stdout, stderr;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct STARTUPINFOEX {
        public STARTUPINFO startup;
        public IntPtr attributes;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct PROCESS_INFORMATION {
        public IntPtr process, thread;
        public uint processId, threadId;
    }

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CreatePipe(out IntPtr read, out IntPtr write, IntPtr security, uint size);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern int CreatePseudoConsole(COORD size, IntPtr input, IntPtr output, uint flags, out IntPtr pseudoConsole);
    [DllImport("kernel32.dll")]
    private static extern void ClosePseudoConsole(IntPtr pseudoConsole);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool InitializeProcThreadAttributeList(IntPtr list, uint count, uint flags, ref IntPtr size);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool UpdateProcThreadAttribute(IntPtr list, uint flags, IntPtr attribute, IntPtr value, IntPtr size, IntPtr previous, IntPtr returnedSize);
    [DllImport("kernel32.dll")]
    private static extern void DeleteProcThreadAttributeList(IntPtr list);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool CreateProcessW(string applicationName, StringBuilder commandLine, IntPtr processAttributes,
        IntPtr threadAttributes, bool inheritHandles, uint creationFlags, IntPtr environment, string currentDirectory,
        ref STARTUPINFOEX startupInfo, out PROCESS_INFORMATION processInformation);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool PeekNamedPipe(IntPtr pipe, IntPtr buffer, uint bufferSize, IntPtr bytesRead, out uint available, IntPtr bytesLeft);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool ReadFile(IntPtr handle, byte[] buffer, uint bytesToRead, out uint bytesRead, IntPtr overlapped);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool WriteFile(IntPtr handle, byte[] buffer, uint bytesToWrite, out uint bytesWritten, IntPtr overlapped);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool GetExitCodeProcess(IntPtr process, out uint exitCode);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool TerminateProcess(IntPtr process, uint exitCode);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(IntPtr handle);
    [DllImport("kernel32.dll")]
    private static extern int ResizePseudoConsole(IntPtr pseudoConsole, COORD size);

    private static void Require(bool success, string stage) {
        if (!success) throw new InvalidOperationException(stage);
    }

    private static string Quote(string value) {
        if (value == null) throw new InvalidOperationException("process_arguments");
        var result = new StringBuilder();
        result.Append('"');
        int slashes = 0;
        foreach (char ch in value) {
            if (ch == '\\') { slashes++; continue; }
            if (ch == '"') {
                result.Append('\\', slashes * 2 + 1);
                result.Append('"');
                slashes = 0;
                continue;
            }
            result.Append('\\', slashes);
            slashes = 0;
            result.Append(ch);
        }
        result.Append('\\', slashes * 2);
        result.Append('"');
        return result.ToString();
    }

    private static string MakeCommandLine(string binary, string[] args) {
        var result = new StringBuilder(Quote(binary));
        foreach (string arg in args) {
            result.Append(' ');
            result.Append(Quote(arg));
        }
        return result.ToString();
    }

    private static void Emit(string value) {
        Console.Out.WriteLine(value);
        Console.Out.Flush();
    }

    private static void Drain(IntPtr output, int maxBytes) {
        int total = 0;
        byte[] buffer = new byte[MaxFrameBytes];
        while (total < maxBytes) {
            uint available;
            if (!PeekNamedPipe(output, IntPtr.Zero, 0, IntPtr.Zero, out available, IntPtr.Zero))
                throw new InvalidOperationException("terminal_read");
            if (available == 0) return;
            uint take = (uint)Math.Min(Math.Min((int)available, buffer.Length), maxBytes - total);
            uint read;
            if (!ReadFile(output, buffer, take, out read, IntPtr.Zero) || read == 0)
                throw new InvalidOperationException("terminal_read");
            Emit("DATA:" + Convert.ToBase64String(buffer, 0, (int)read));
            total += (int)read;
        }
    }

    private static int ChildExitCode(IntPtr process) {
        uint code;
        if (!GetExitCodeProcess(process, out code)) throw new InvalidOperationException("ui_exit_status");
        return unchecked((int)code);
    }

    private static void WriteInput(IntPtr input, byte[] bytes) {
        if (bytes.Length > MaxFrameBytes) throw new InvalidOperationException("terminal_input_bound");
        int offset = 0;
        while (offset < bytes.Length) {
            uint written;
            if (!WriteFile(input, bytes, (uint)bytes.Length, out written, IntPtr.Zero) || written == 0)
                throw new InvalidOperationException("terminal_input");
            offset += (int)written;
        }
    }

    private static void CloseAndDrain(IntPtr pseudoConsole, IntPtr output) {
        var closer = new Thread(() => ClosePseudoConsole(pseudoConsole));
        closer.IsBackground = true;
        closer.Start();
        DateTime deadline = DateTime.UtcNow.AddSeconds(5);
        while (!closer.Join(20)) {
            Drain(output, MaxDrainBytes);
            if (DateTime.UtcNow >= deadline) throw new InvalidOperationException("conpty_close_timeout");
        }
        Drain(output, MaxDrainBytes);
    }

    public static int Run(string binary, string[] args, string currentDirectory) {
        IntPtr inputRead = IntPtr.Zero, inputWrite = IntPtr.Zero;
        IntPtr outputRead = IntPtr.Zero, outputWrite = IntPtr.Zero;
        IntPtr pseudoConsole = IntPtr.Zero, attributeList = IntPtr.Zero;
        IntPtr child = IntPtr.Zero, childThread = IntPtr.Zero;
        bool attributeListReady = false;
        string failure = "conpty_start";
        try {
            Require(CreatePipe(out inputRead, out inputWrite, IntPtr.Zero, 0), "conpty_pipe");
            Require(CreatePipe(out outputRead, out outputWrite, IntPtr.Zero, 0), "conpty_pipe");
            int hr = CreatePseudoConsole(new COORD(80, 18), inputRead, outputWrite, 0, out pseudoConsole);
            if (hr < 0) throw new InvalidOperationException("conpty_create");

            IntPtr attributeBytes = IntPtr.Zero;
            InitializeProcThreadAttributeList(IntPtr.Zero, 1, 0, ref attributeBytes);
            if (attributeBytes == IntPtr.Zero) throw new InvalidOperationException("conpty_attributes");
            attributeList = Marshal.AllocHGlobal(attributeBytes);
            if (!InitializeProcThreadAttributeList(attributeList, 1, 0, ref attributeBytes))
                throw new InvalidOperationException("conpty_attributes");
            attributeListReady = true;
            if (!UpdateProcThreadAttribute(attributeList, 0, new IntPtr(PseudoConsoleAttribute), pseudoConsole,
                    new IntPtr(IntPtr.Size), IntPtr.Zero, IntPtr.Zero))
                throw new InvalidOperationException("conpty_attributes");

            var startup = new STARTUPINFOEX();
            startup.startup.cb = (uint)Marshal.SizeOf(typeof(STARTUPINFOEX));
            // With redirected parent stdio, null child handles alone can inherit the
            // wrong handles. STARTF_USESTDHANDLES makes ConPTY supply child stdio;
            // see https://github.com/microsoft/terminal/discussions/15814 and
            // https://learn.microsoft.com/windows/console/creating-a-pseudoconsole-session.
            startup.startup.flags = StartfUseStdHandles;
            startup.attributes = attributeList;
            var commandLine = new StringBuilder(MakeCommandLine(binary, args));
            PROCESS_INFORMATION info;
            if (!CreateProcessW(null, commandLine, IntPtr.Zero, IntPtr.Zero, false, ExtendedStartupInfoPresent,
                    IntPtr.Zero, currentDirectory, ref startup, out info))
                throw new InvalidOperationException("conpty_process");
            child = info.process;
            childThread = info.thread;
            CloseHandle(inputRead); inputRead = IntPtr.Zero;
            CloseHandle(outputWrite); outputWrite = IntPtr.Zero;

            Emit("READY");
            while (true) {
                string command = Console.In.ReadLine();
                if (command == null) {
                    TerminateProcess(child, 1);
                    WaitForSingleObject(child, 5000);
                    break;
                }
                if (command.Length > 100000) throw new InvalidOperationException("protocol_bound");
                if (command == "POLL") {
                    Drain(outputRead, MaxDrainBytes);
                    Emit("END");
                    continue;
                }
                if (command == "WAIT") {
                    Drain(outputRead, MaxDrainBytes);
                    if (WaitForSingleObject(child, 0) == 0) Emit("EXIT:" + ChildExitCode(child));
                    else Emit("RUNNING");
                    continue;
                }
                if (command == "TERMINATE") {
                    if (WaitForSingleObject(child, 0) != 0) TerminateProcess(child, 1);
                    WaitForSingleObject(child, 5000);
                    Drain(outputRead, MaxDrainBytes);
                    Emit("EXIT:" + ChildExitCode(child));
                    continue;
                }
                if (command.StartsWith("WRITE:", StringComparison.Ordinal)) {
                    byte[] bytes;
                    try { bytes = Convert.FromBase64String(command.Substring(6)); }
                    catch { throw new InvalidOperationException("protocol_input"); }
                    WriteInput(inputWrite, bytes);
                    Emit("OK");
                    continue;
                }
                if (command.StartsWith("RESIZE:", StringComparison.Ordinal)) {
                    string[] parts = command.Substring(7).Split(':');
                    int width, height;
                    if (parts.Length != 2 || !Int32.TryParse(parts[0], out width) || !Int32.TryParse(parts[1], out height) ||
                        width < 20 || width > 500 || height < 8 || height > 200)
                        throw new InvalidOperationException("terminal_resize");
                    hr = ResizePseudoConsole(pseudoConsole, new COORD((short)width, (short)height));
                    if (hr < 0) throw new InvalidOperationException("terminal_resize");
                    Emit("OK");
                    continue;
                }
                if (command == "CLOSE") {
                    if (WaitForSingleObject(child, 0) != 0) {
                        TerminateProcess(child, 1);
                        WaitForSingleObject(child, 5000);
                    }
                    Drain(outputRead, MaxDrainBytes);
                    if (inputWrite != IntPtr.Zero) { CloseHandle(inputWrite); inputWrite = IntPtr.Zero; }
                    CloseAndDrain(pseudoConsole, outputRead);
                    pseudoConsole = IntPtr.Zero;
                    Emit("CLOSED");
                    break;
                }
                throw new InvalidOperationException("protocol_command");
            }
            failure = null;
        } catch (Exception ex) {
            failure = ex is InvalidOperationException ? ex.Message : "conpty_host";
            Emit("ERROR:" + failure);
        } finally {
            if (child != IntPtr.Zero && WaitForSingleObject(child, 0) != 0) {
                TerminateProcess(child, 1);
                WaitForSingleObject(child, 5000);
            }
            if (pseudoConsole != IntPtr.Zero) {
                if (inputWrite != IntPtr.Zero) { CloseHandle(inputWrite); inputWrite = IntPtr.Zero; }
                try { CloseAndDrain(pseudoConsole, outputRead); } catch { ClosePseudoConsole(pseudoConsole); }
            }
            if (attributeListReady) DeleteProcThreadAttributeList(attributeList);
            if (attributeList != IntPtr.Zero) Marshal.FreeHGlobal(attributeList);
            if (inputRead != IntPtr.Zero) CloseHandle(inputRead);
            if (inputWrite != IntPtr.Zero) CloseHandle(inputWrite);
            if (outputRead != IntPtr.Zero) CloseHandle(outputRead);
            if (outputWrite != IntPtr.Zero) CloseHandle(outputWrite);
            if (childThread != IntPtr.Zero) CloseHandle(childThread);
            if (child != IntPtr.Zero) CloseHandle(child);
        }
        return failure == null ? 0 : 1;
    }
}
'@

try {
    Add-Type -TypeDefinition $source -Language CSharp -ErrorAction Stop
    $childArguments = [string[]]@('ui', $Task, '--store', $Store)
    $result = [NativeTuiConPty]::Run($Binary, $childArguments, $WorkingDirectory)
    exit $result
} catch {
    [Console]::Out.WriteLine('ERROR:conpty_helper')
    [Console]::Out.Flush()
    exit 1
}
