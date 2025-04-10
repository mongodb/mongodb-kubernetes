package multicluster

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetRsNamefromMultiStsName(t *testing.T) {
	tests := []struct {
		inp  string
		want string
	}{
		{
			inp:  "foo-bar-test-1",
			want: "foo-bar-test",
		},
		{
			inp:  "foo-0",
			want: "foo",
		},
		{
			inp:  "foo-bar-1-2",
			want: "foo-bar-1",
		},
	}

	for _, tt := range tests {
		got := GetRsNamefromMultiStsName(tt.inp)
		assert.Equal(t, tt.want, got)
	}
}

func TestGetRsNamefromMultiStsNamePanic(t *testing.T) {
	tests := []struct {
		inp string
	}{
		{
			inp: "",
		},
		{
			inp: "-1",
		},
	}

	for _, tt := range tests {
		assert.Panics(t, func() { GetRsNamefromMultiStsName(tt.inp) }, "The code did not panic")
	}
}

func TestLoadKubeConfigFile(t *testing.T) {
	inp := []struct {
		server  string
		name    string
		ca      string
		cluster string
	}{
		{
			server: "https://api.e2e.cluster1.mongokubernetes.com",
			name:   "e2e.cluster1.mongokubernetes.com",
			ca:     "abcdbbd",
		},
		{
			server: "https://api.e2e.cluster2.mongokubernetes.com",
			name:   "e2e.cluster2.mongokubernetes.com",
			ca:     "njcdkn",
		},
		{
			server: "https://api.e2e.cluster3.mongokubernetes.com",
			name:   "e2e.cluster3.mongokubernetes.com",
			ca:     "nxjknjk",
		},
	}

	str := fmt.Sprintf(
		`
apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: %s
    server: %s
  name: %s
- cluster:
    certificate-authority-data: %s
    server: %s
  name: %s
- cluster:
    certificate-authority-data: %s
    server: %s
  name: %s
contexts:
- context:
    cluster: e2e.cluster1.mongokubernetes.com
    namespace: a-1661872869-pq35wlt3zzz
    user: e2e.cluster1.mongokubernetes.com
  name: e2e.cluster1.mongokubernetes.com
- context:
    cluster: e2e.cluster2.mongokubernetes.com
    namespace: a-1661872869-pq35wlt3zzz
    user: e2e.cluster2.mongokubernetes.com
  name: e2e.cluster2.mongokubernetes.com
- context:
    cluster: e2e.cluster3.mongokubernetes.com
    namespace: a-1661872869-pq35wlt3zzz
    user: e2e.cluster3.mongokubernetes.com
  name: e2e.cluster3.mongokubernetes.com
kind: Config
users:
- name: e2e.cluster1.mongokubernetes.com
  user:
    token: eyJhbGciOi
- name: e2e.cluster2.mongokubernetes.com
  user:
    token: eyJhbGc
- name: e2e.cluster3.mongokubernetes.com
  user:
    token: njncdnjn`, inp[0].ca, inp[0].server, inp[0].name, inp[1].ca,
		inp[1].server, inp[1].name, inp[2].ca, inp[1].server, inp[1].name)

	k := KubeConfig{
		Reader: strings.NewReader(str),
	}

	arr, err := k.LoadKubeConfigFile()
	if err != nil {
		t.Error(err)
	}

	for n, e := range arr.Contexts {
		assert.Equal(t, inp[n].name, e.Name)
		assert.Equal(t, inp[n].name, e.Context.Cluster)
	}
}
