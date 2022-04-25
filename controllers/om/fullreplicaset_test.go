package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetermineNextProcessIdStartingPoint(t *testing.T) {

	t.Run("New id should be higher than any other id", func(t *testing.T) {
		desiredProcesses := []Process{
			{
				"name": "p-0",
			},
			{
				"name": "p-1",
			},
			{
				"name": "p-2",
			},
			{
				"name": "p-3",
			},
		}

		existingIds := map[string]int{
			"p-0": 0,
			"p-1": 1,
			"p-2": 2,
			"p-3": 3,
		}

		assert.Equal(t, 4, determineNextProcessIdStartingPoint(desiredProcesses, existingIds))
	})

	t.Run("New id should be higher than other ids even if there are gaps in between", func(t *testing.T) {
		desiredProcesses := []Process{
			{
				"name": "p-0",
			},
			{
				"name": "p-1",
			},
			{
				"name": "p-2",
			},
		}

		existingIds := map[string]int{
			"p-0": 0,
			"p-1": 5,
			"p-2": 3,
		}

		assert.Equal(t, 6, determineNextProcessIdStartingPoint(desiredProcesses, existingIds))
	})
}
