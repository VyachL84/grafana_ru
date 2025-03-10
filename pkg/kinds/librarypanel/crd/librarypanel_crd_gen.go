// Code generated - EDITING IS FUTILE. DO NOT EDIT.
//
// Generated by:
//     kinds/gen.go
// Using jennies:
//     CRDTypesJenny
//
// Run 'make gen-cue' from repository root to regenerate.

package crd

import (
	_ "embed"

	"github.com/grafana/grafana/pkg/kinds/librarypanel"
	"github.com/grafana/grafana/pkg/kindsys/k8ssys"
)

// The CRD YAML representation of the LibraryPanel kind.
//
//go:embed librarypanel.crd.yml
var CRDYaml []byte

// LibraryPanel is the Go CRD representation of a single LibraryPanel object.
// It implements [runtime.Object], and is used in k8s scheme construction.
type LibraryPanel struct {
	k8ssys.Base[librarypanel.LibraryPanel]
}

// LibraryPanelList is the Go CRD representation of a list LibraryPanel objects.
// It implements [runtime.Object], and is used in k8s scheme construction.
type LibraryPanelList struct {
	k8ssys.ListBase[librarypanel.LibraryPanel]
}
