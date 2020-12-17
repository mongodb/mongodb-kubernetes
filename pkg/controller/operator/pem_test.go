package operator

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/pem"

	"github.com/stretchr/testify/assert"
)

func TestGetPEMHashIsDeterministic(t *testing.T) {
	pemCollection := pem.Collection{
		PemFiles: map[string]pem.File{
			"myhostname1": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
			"myhostname2": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}
	firstHash, err := pemCollection.GetHash()
	assert.NoError(t, err)

	// modify the PEM collection and check the hash is different
	pemCollection.PemFiles["myhostname3"] = pem.File{
		PrivateKey:  "thirdey",
		Certificate: "thirdcert",
	}
	secondHash, err := pemCollection.GetHash()
	assert.NoError(t, err)
	assert.NotEqual(t, firstHash, secondHash)

	// revert the changes to the PEM collection and check the hash is the same
	delete(pemCollection.PemFiles, "myhostname3")
	thirdHash, err := pemCollection.GetHash()
	assert.NoError(t, err)
	assert.Equal(t, firstHash, thirdHash)
}

func TestMergeEntryOverwritesOldSecret(t *testing.T) {
	p := pem.Collection{
		PemFiles: map[string]pem.File{
			"myhostname": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}

	secretData := pem.File{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.MergeEntry("myhostname", secretData)
	assert.Equal(t, "mykey", p.PemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "mycert", p.PemFiles["myhostname"].Certificate)
}

func TestMergeEntryOnlyCertificate(t *testing.T) {
	p := pem.Collection{
		PemFiles: map[string]pem.File{
			"myhostname": {
				PrivateKey: "mykey",
			},
		},
	}

	secretData := pem.File{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.MergeEntry("myhostname", secretData)
	assert.Equal(t, "mykey", p.PemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "oldcert", p.PemFiles["myhostname"].Certificate)
}

func TestMergeEntryPreservesOldSecret(t *testing.T) {
	p := pem.Collection{
		PemFiles: map[string]pem.File{
			"myexistinghostname": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}

	secretData := pem.File{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.MergeEntry("myhostname", secretData)
	assert.Equal(t, "oldkey", p.PemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "oldcert", p.PemFiles["myhostname"].Certificate)
	assert.Equal(t, "mykey", p.PemFiles["myexistinghostname"].PrivateKey)
	assert.Equal(t, "mycert", p.PemFiles["myexistinghostname"].Certificate)
}
