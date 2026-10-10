package main

import "testing"

func TestInstallProcessBuilderReservationAndAnnotations(t *testing.T) {
	if !processInstallReserve("tenant-a") || processInstallReserve("tenant-a") || !processInstallReserve("tenant-b") {
		t.Fatal("one configuration build per target")
	}
	processInstallRelease("tenant-a")
	processInstallRelease("tenant-b")
	if !processInstallReserve("tenant-a") {
		t.Fatal("a released target must accept a new build")
	}
	processInstallRelease("tenant-a")
	if got := annotationsOf(processInstallTool); got.ReadOnly || !got.Destructive || !got.Idempotent || got.OpenWorld {
		t.Fatalf("annotations %#v", got)
	}
	if !requiresConfirmation(processInstallTool) {
		t.Fatal("install-process-builder writes and must not run by its raw name")
	}
}
