package test

type ClusterDomains struct {
	MongosExternalDomain       string
	ConfigServerExternalDomain string
	ShardsExternalDomain       string
	SingleClusterDomain        string
}

var (
	ClusterLocalDomains = ClusterDomains{
		MongosExternalDomain:       "cluster.local",
		ConfigServerExternalDomain: "cluster.local",
		ShardsExternalDomain:       "cluster.local",
		SingleClusterDomain:        "cluster.local",
	}
	ExampleExternalClusterDomains = ClusterDomains{
		MongosExternalDomain:       "mongos.mongodb.com",
		ConfigServerExternalDomain: "config.mongodb.com",
		ShardsExternalDomain:       "shards.mongodb.com",
		SingleClusterDomain:        "single.mongodb.com",
	}
	ExampleAccessWithNoExternalDomain = ClusterDomains{
		MongosExternalDomain:       "my-namespace.svc.cluster.local",
		ConfigServerExternalDomain: "my-namespace.svc.cluster.local",
		ShardsExternalDomain:       "my-namespace.svc.cluster.local",
		SingleClusterDomain:        "my-namespace.svc.cluster.local",
	}
	SingleExternalClusterDomains = ClusterDomains{
		MongosExternalDomain:       "single.mongodb.com",
		ConfigServerExternalDomain: "single.mongodb.com",
		ShardsExternalDomain:       "single.mongodb.com",
		SingleClusterDomain:        "single.mongodb.com",
	}
	NoneExternalClusterDomains = ClusterDomains{}
)
