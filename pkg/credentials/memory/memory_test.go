package memory_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
)

func newStore(*testing.T) credentials.BlobStore { return memory.New() }

func TestBlobStore(t *testing.T) { credentialstest.RunBlobStore(t, newStore) }

func TestVault(t *testing.T) { credentialstest.RunVault(t, newStore) }
