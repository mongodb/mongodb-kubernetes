package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetPEMHashIsDeterministic(t *testing.T) {
	pem := pemCollection{
		pemFiles: map[string]pemFile{
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
	firstHash, err := pem.getHash()
	assert.NoError(t, err)

	// modify the PEM collection and check the hash is different
	pem.pemFiles["myhostname3"] = pemFile{
		PrivateKey:  "thirdey",
		Certificate: "thirdcert",
	}
	secondHash, err := pem.getHash()
	assert.NoError(t, err)
	assert.NotEqual(t, firstHash, secondHash)

	// revert the changes to the PEM collection and check the hash is the same
	delete(pem.pemFiles, "myhostname3")
	thirdHash, err := pem.getHash()
	assert.NoError(t, err)
	assert.Equal(t, firstHash, thirdHash)
}

func TestMergeEntryOverwritesOldSecret(t *testing.T) {
	p := pemCollection{
		pemFiles: map[string]pemFile{
			"myhostname": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}

	secretData := pemFile{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.mergeEntry("myhostname", secretData)
	assert.Equal(t, "mykey", p.pemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "mycert", p.pemFiles["myhostname"].Certificate)
}

func TestMergeEntryOnlyCertificate(t *testing.T) {
	p := pemCollection{
		pemFiles: map[string]pemFile{
			"myhostname": {
				PrivateKey: "mykey",
			},
		},
	}

	secretData := pemFile{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.mergeEntry("myhostname", secretData)
	assert.Equal(t, "mykey", p.pemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "oldcert", p.pemFiles["myhostname"].Certificate)
}

func TestMergeEntryPreservesOldSecret(t *testing.T) {
	p := pemCollection{
		pemFiles: map[string]pemFile{
			"myexistinghostname": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}

	secretData := pemFile{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.mergeEntry("myhostname", secretData)
	assert.Equal(t, "oldkey", p.pemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "oldcert", p.pemFiles["myhostname"].Certificate)
	assert.Equal(t, "mykey", p.pemFiles["myexistinghostname"].PrivateKey)
	assert.Equal(t, "mycert", p.pemFiles["myexistinghostname"].Certificate)
}
