package images

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestMongodbProxyImage(t *testing.T) {
	const related = "RELATED_IMAGE_MDB_MONGODB_PROXY_IMAGE_1_15_0"

	tests := map[string]struct {
		urls ImageUrls
		want string
	}{
		"not set": {
			urls: ImageUrls{},
			want: "",
		},
		"a tag is kept": {
			urls: ImageUrls{util.MongodbProxyImageEnv: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy:1.15.0"},
			want: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy:1.15.0",
		},
		"a registry with a port": {
			urls: ImageUrls{util.MongodbProxyImageEnv: "localhost:5000/mongodb-kubernetes-mongodb-proxy:proxydev-1"},
			want: "localhost:5000/mongodb-kubernetes-mongodb-proxy:proxydev-1",
		},
		"the related image of the version wins (a digest in a disconnected installation)": {
			urls: ImageUrls{
				util.MongodbProxyImageEnv: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy:1.15.0",
				related:                   "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy@sha256:abc",
			},
			want: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy@sha256:abc",
		},
		"the related image of another version is not used": {
			urls: ImageUrls{
				util.MongodbProxyImageEnv: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy:1.16.0",
				related:                   "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy@sha256:abc",
			},
			want: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy:1.16.0",
		},
		"a digest is kept": {
			urls: ImageUrls{util.MongodbProxyImageEnv: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy@sha256:abc"},
			want: "quay.io/mongodb/mongodb-kubernetes-mongodb-proxy@sha256:abc",
		},
		"no tag, only a registry port": {
			urls: ImageUrls{util.MongodbProxyImageEnv: "localhost:5000/mongodb-kubernetes-mongodb-proxy"},
			want: "localhost:5000/mongodb-kubernetes-mongodb-proxy",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, MongodbProxyImage(tt.urls))
		})
	}
}
