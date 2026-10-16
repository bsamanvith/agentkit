// Package coretools provides built-in toolset factories that need no
// external account: [Math] (arithmetic and statistics) and [Files] (list,
// read, search and edit files under one root folder).
//
// Like [toolset.Demo] they are ordinary [toolset.Factory] values: the binary
// adds them to its catalog and config picks them by type ("math", "files").
//
// Files confines every path to its root with [os.Root], so neither ".." nor a
// symlink reaches outside it. Its one writing tool, edit_file, can be left out
// with the spec's exclude list for a read-only toolset.
package coretools
