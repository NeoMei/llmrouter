package main

import (
	"strings"
	"testing"
)

func TestReviewedManifestRejectsUndrainedBalanceCutoverAndDuplicateUsers(t *testing.T) {
	u := migrationUser{LocalUserID: 7, ExpectedUsername: "original-admin", ExpectedQuota: 100, ExpectedRole: 10, ExpectedAuthVersion: 1, ImportBalance: true}
	in := migrationManifest{BatchID: "reviewed", Users: []migrationUser{u}}
	if validateManifest(in) == nil {
		t.Fatal("undrained balance migration accepted")
	}
	in.Drained = true
	if validateManifest(in) == nil {
		t.Fatal("fresh CLI process incorrectly proves another process drained")
	}
	in.OfflineEvidence = &offlineCutoverEvidence{AllWritersStopped: true, BatchReconciled: true, CachesReconciled: true, BackupSHA256: strings.Repeat("a", 64), SnapshotSHA256: manifestSnapshotDigest(in), EvidenceRef: "reviewed-offline-reconciliation.md"}
	if err := validateManifest(in); err != nil {
		t.Fatal(err)
	}
	in.Users = append(in.Users, u)
	if validateManifest(in) == nil {
		t.Fatal("ambiguous duplicate migration accepted")
	}
}

func TestOfflineEvidenceIsBoundToReviewedFinancialSnapshot(t *testing.T) {
	in := migrationManifest{BatchID: "batch", Drained: true, Users: []migrationUser{{LocalUserID: 7, ExpectedUsername: "original", ExpectedQuota: 100, ExpectedRole: 1, ExpectedAuthVersion: 1, ImportBalance: true}}}
	in.OfflineEvidence = &offlineCutoverEvidence{AllWritersStopped: true, BatchReconciled: true, CachesReconciled: true, BackupSHA256: strings.Repeat("b", 64), SnapshotSHA256: manifestSnapshotDigest(in), EvidenceRef: "offline-receipt"}
	if err := validateManifest(in); err != nil {
		t.Fatal(err)
	}
	in.Users[0].ExpectedQuota = 101
	if validateManifest(in) == nil {
		t.Fatal("changed balance reused old offline receipt")
	}
}
