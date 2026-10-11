package delivery

// LiveBackendUnsupported names the current delivery boundary. The native
// workspace backend has only best-effort capture and cannot exclude writers
// that already hold writable descriptors or mappings.
const LiveBackendUnsupported = "UNSUPPORTED_STRONG_EXCLUSIVITY; CANDIDATE_PATCH_ONLY"
