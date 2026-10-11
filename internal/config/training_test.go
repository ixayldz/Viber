package config

import "testing"

func TestTrainingConsentCannotBeGrantedByProjectOrTelemetry(t *testing.T) {
	yes, no := true, false
	user := File{SchemaVersion: 1, Restrictions: Privacy{TelemetryExport: &yes}}
	project := File{SchemaVersion: 1, Restrictions: Privacy{TrainingExport: &yes}}
	r, err := Resolve(&user, &project)
	if err != nil {
		t.Fatal(err)
	}
	if r.AdmitTrainingExport() == nil {
		t.Fatal("project or telemetry granted training consent")
	}
	user.Restrictions.TrainingExport = &yes
	r, err = Resolve(&user, &project)
	if err != nil || r.AdmitTrainingExport() != nil {
		t.Fatal("separate user opt-in denied", err)
	}
	project.Restrictions.TrainingExport = &no
	r, err = Resolve(&user, &project)
	if err != nil {
		t.Fatal(err)
	}
	if r.AdmitTrainingExport() == nil {
		t.Fatal("user opt-in overrode project denial")
	}
}
