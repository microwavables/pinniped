// Copyright 2020 the Pinniped contributors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package testlib

import "testing"

// skipUnlessIntegration skips the current test if `-short` has been passed to `go test`.
func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test because of '-short' flag")
	}
}
