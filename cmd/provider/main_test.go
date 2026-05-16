/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPodNamespaceFromFile(t *testing.T) {
	cases := map[string]struct {
		reason string
		setup  func(dir string) string
		want   string
	}{
		"SuccessfulRead": {
			reason: "Should return the namespace from the file",
			setup: func(dir string) string {
				path := filepath.Join(dir, "namespace")
				if err := os.WriteFile(path, []byte("my-namespace\n"), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: "my-namespace",
		},
		"EmptyFile": {
			reason: "Should fall back to default namespace when the file is empty",
			setup: func(dir string) string {
				path := filepath.Join(dir, "namespace")
				if err := os.WriteFile(path, []byte(""), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: defaultLeaseNamespaceFallBack,
		},
		"WhitespaceOnly": {
			reason: "Should fall back to default namespace when the file contains only whitespace",
			setup: func(dir string) string {
				path := filepath.Join(dir, "namespace")
				if err := os.WriteFile(path, []byte("   \n\t\n"), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: defaultLeaseNamespaceFallBack,
		},
		"FileNotFound": {
			reason: "Should fall back to default namespace when the file does not exist",
			setup: func(dir string) string {
				return filepath.Join(dir, "nonexistent")
			},
			want: defaultLeaseNamespaceFallBack,
		},
		"PathOutsideAllowedPrefix": {
			reason: "Should fall back to default namespace when the path is outside the allowed prefix",
			setup: func(dir string) string {
				return "/etc/passwd"
			},
			want: defaultLeaseNamespaceFallBack,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			oldPrefix := namespacePathPrefix
			namespacePathPrefix = dir + string(filepath.Separator)
			defer func() { namespacePathPrefix = oldPrefix }()
			path := tc.setup(dir)
			got := podNamespaceFromFile(path)
			if got != tc.want {
				t.Errorf("\n%s\npodNamespaceFromFile(...): want %q, got %q\n", tc.reason, tc.want, got)
			}
		})
	}
}
