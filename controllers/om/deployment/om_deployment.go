package deployment

import (
	"fmt"
)

// Link returns the deployment link given the baseUrl and groupId.
func Link(url, groupId string) string {
	return fmt.Sprintf("%s/v2/%s", url, groupId)
}
