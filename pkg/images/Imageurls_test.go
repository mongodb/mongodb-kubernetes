package images

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
)

func TestReplaceImageTagOrDigestToTag(t *testing.T) {
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:9876-54321", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-agent:1234-567", "9876-54321"))
	assert.Equal(t, "docker.io/mongodb/mongodb-enterprise-server:9876-54321", replaceImageTagOrDigestToTag("docker.io/mongodb/mongodb-enterprise-server:1234-567", "9876-54321"))
	assert.Equal(t, "quay.io/mongodb/mongodb-agent:9876-54321", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-agent@sha256:6a82abae27c1ba1133f3eefaad71ea318f8fa87cc57fe9355d6b5b817ff97f1a", "9876-54321"))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:some-tag", replaceImageTagOrDigestToTag("quay.io/mongodb/mongodb-enterprise-database:45678", "some-tag"))
	assert.Equal(t, "quay.io:3000/mongodb/mongodb-enterprise-database:some-tag", replaceImageTagOrDigestToTag("quay.io:3000/mongodb/mongodb-enterprise-database:45678", "some-tag"))
}

func TestContainerImage(t *testing.T) {
	initDatabaseRelatedImageEnv1 := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.InitDatabaseImageUrlEnv)
	initDatabaseRelatedImageEnv2 := fmt.Sprintf("RELATED_IMAGE_%s_12_0_4_7554_1", util.InitDatabaseImageUrlEnv)
	initDatabaseRelatedImageEnv3 := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0_b20220912000000", util.InitDatabaseImageUrlEnv)

	t.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	t.Setenv(initDatabaseRelatedImageEnv1, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd")
	t.Setenv(initDatabaseRelatedImageEnv2, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:b631ee886bb49ba8d7b90bb003fe66051dadecbc2ac126ac7351221f4a7c377c")
	t.Setenv(initDatabaseRelatedImageEnv3, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:f1a7f49cd6533d8ca9425f25cdc290d46bb883997f07fac83b66cc799313adad")

	// there is no related image for 0.0.1
	imageUrls := LoadImageUrlsFromEnv()
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:0.0.1", ContainerImage(imageUrls, util.InitDatabaseImageUrlEnv, "0.0.1"))
	// for 10.2.25.6008-1 there is no RELATED_IMAGE variable set, so we use input instead of digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:10.2.25.6008-1", ContainerImage(imageUrls, util.InitDatabaseImageUrlEnv, "10.2.25.6008-1"))
	// for following versions we set RELATED_IMAGE_MONGODB_IMAGE_* env variables to sha256 digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd", ContainerImage(imageUrls, util.InitDatabaseImageUrlEnv, "1.0.0"))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:b631ee886bb49ba8d7b90bb003fe66051dadecbc2ac126ac7351221f4a7c377c", ContainerImage(imageUrls, util.InitDatabaseImageUrlEnv, "12.0.4.7554-1"))
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database@sha256:f1a7f49cd6533d8ca9425f25cdc290d46bb883997f07fac83b66cc799313adad", ContainerImage(imageUrls, util.InitDatabaseImageUrlEnv, "2.0.0-b20220912000000"))

	// env var has input already, so it is replaced
	t.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb:12.0.4.7554-1")
	imageUrls = LoadImageUrlsFromEnv()
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb:10.2.25.6008-1", ContainerImage(imageUrls, util.InitAppdbImageUrlEnv, "10.2.25.6008-1"))

	// env var has input already, but there is related image with this input
	t.Setenv(fmt.Sprintf("RELATED_IMAGE_%s_12_0_4_7554_1", util.InitAppdbImageUrlEnv), "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c")
	imageUrls = LoadImageUrlsFromEnv()
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c", ContainerImage(imageUrls, util.InitAppdbImageUrlEnv, "12.0.4.7554-1"))

	t.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:608daf56296c10c9bd02cc85bb542a849e9a66aff0697d6359b449540696b1fd")
	imageUrls = LoadImageUrlsFromEnv()
	// env var has input already as digest, but there is related image with this input
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb@sha256:a48829ce36bf479dc25a4de79234c5621b67beee62ca98a099d0a56fdb04791c", ContainerImage(imageUrls, util.InitAppdbImageUrlEnv, "12.0.4.7554-1"))
	// env var has input already as digest, there is no related image with this input, so we use input instead of digest
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-appdb:1.2.3", ContainerImage(imageUrls, util.InitAppdbImageUrlEnv, "1.2.3"))

	t.Setenv(util.OpsManagerImageUrl, "quay.io:3000/mongodb/ops-manager-kubernetes")
	imageUrls = LoadImageUrlsFromEnv()
	assert.Equal(t, "quay.io:3000/mongodb/ops-manager-kubernetes:1.2.3", ContainerImage(imageUrls, util.OpsManagerImageUrl, "1.2.3"))

	t.Setenv(util.OpsManagerImageUrl, "localhost/mongodb/ops-manager-kubernetes")
	imageUrls = LoadImageUrlsFromEnv()
	assert.Equal(t, "localhost/mongodb/ops-manager-kubernetes:1.2.3", ContainerImage(imageUrls, util.OpsManagerImageUrl, "1.2.3"))

	t.Setenv(util.OpsManagerImageUrl, "mongodb")
	imageUrls = LoadImageUrlsFromEnv()
	assert.Equal(t, "mongodb:1.2.3", ContainerImage(imageUrls, util.OpsManagerImageUrl, "1.2.3"))
}

func TestGetAppDBImage(t *testing.T) {
	// Note: if no construct.DefaultImageType is given, we will default to ubi8
	tests := []struct {
		name        string
		input       string
		annotations map[string]string
		want        string
		setupEnvs   func(t *testing.T)
	}{
		{
			name:  "Getting official image",
			input: "4.2.11-ubi8",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
			},
		},
		{
			name:  "Getting official image without suffix",
			input: "4.2.11",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
			},
		},
		{
			name:  "Getting official image keep suffix",
			input: "4.2.11-something",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-something",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
			},
		},
		{
			name:  "Getting official image with legacy suffix",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
			},
		},
		{
			name:  "Getting official image with legacy image",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:4.2.11-ent",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.DeprecatedImageAppdbUbiUrl)
			},
		},
		{
			name:  "Getting official image with related image from deprecated URL",
			input: "4.2.11-ubi8",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ubi8", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8")
				t.Setenv(construct.MongoDBImageType, "ubi8")
				t.Setenv(construct.MongodbImageEnv, util.DeprecatedImageAppdbUbiUrl)
				t.Setenv(construct.MongodbRepoUrl, construct.OfficialMongodbRepoUrls[1])
			},
		},
		{
			name:  "Getting official image with related image with ent suffix even if old related image exists",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8",
			setupEnvs: func(t *testing.T) {
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ubi8", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8")
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ent", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ent")
				t.Setenv(construct.MongoDBImageType, "ubi8")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
				t.Setenv(construct.MongodbRepoUrl, construct.OfficialMongodbRepoUrls[1])
			},
		},
		{
			name:  "Getting deprecated image with related image from official URL. We do not replace -ent because the url is not a deprecated one we want to replace",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-appdb-database-ubi:4.2.11-ent",
			setupEnvs: func(t *testing.T) {
				t.Setenv("RELATED_IMAGE_MONGODB_IMAGE_4_2_11_ubi8", "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8")
				t.Setenv(construct.MongodbImageEnv, util.DeprecatedImageAppdbUbiUrl)
				t.Setenv(construct.MongodbRepoUrl, construct.OfficialMongodbRepoUrls[1])
			},
		},
		{
			name:  "Getting official image with legacy suffix but stopping migration",
			input: "4.2.11-ent",
			want:  "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ent",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
				t.Setenv(util.MdbAppdbAssumeOldFormat, "true")
			},
		},
		{
			name:  "Getting official image with legacy suffix on static architecture",
			input: "4.2.11-ent",
			annotations: map[string]string{
				"mongodb.com/v1.architecture": string(architectures.Static),
			},
			want: "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi9",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
			},
		},
		{
			name:  "Getting official ubi9 image with ubi8 suffix on static architecture",
			input: "4.2.11-ubi8",
			annotations: map[string]string{
				"mongodb.com/v1.architecture": string(architectures.Static),
			},
			want: "quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi9",
			setupEnvs: func(t *testing.T) {
				t.Setenv(construct.MongodbRepoUrl, "quay.io/mongodb")
				t.Setenv(construct.MongodbImageEnv, util.OfficialEnterpriseServerImageUrl)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnvs(t)
			imageUrlsMock := LoadImageUrlsFromEnv()
			assert.Equalf(t, tt.want, GetOfficialImage(imageUrlsMock, tt.input, tt.annotations), "getOfficialImage(%v)", tt.input)
		})
	}
}

func TestIsEnterpriseImage(t *testing.T) {
	tests := []struct {
		name           string
		imageURL       string
		expectedResult bool
	}{
		{
			name:           "Enterprise Image",
			imageURL:       "myregistry.com/mongo/mongodb-enterprise-server:latest",
			expectedResult: true,
		},
		{
			name:           "Community Image",
			imageURL:       "myregistry.com/mongo/mongodb-community-server:latest",
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsEnterpriseImage(tt.imageURL)

			if result != tt.expectedResult {
				t.Errorf("expected %v, got %v", tt.expectedResult, result)
			}
		})
	}
}
