package status

// Part is the logical constant for specific field in status in the MongoDBOpsManager
type Part int

const (
	AppDb Part = iota
	OpsManager
	Backup
	None
)
