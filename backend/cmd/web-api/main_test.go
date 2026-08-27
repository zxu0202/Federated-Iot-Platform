package main

import (
	"testing"
	"time"
)

func TestLeaseRecoveryIntervalIsBoundedByLease(t *testing.T) {
	if observed := leaseRecoveryInterval(10*time.Second, 60*time.Second); observed != 10*time.Second {
		t.Fatalf("recovery interval = %s, want heartbeat interval", observed)
	}
	if observed := leaseRecoveryInterval(45*time.Second, 60*time.Second); observed != 30*time.Second {
		t.Fatalf("recovery interval = %s, want half lease", observed)
	}
}
