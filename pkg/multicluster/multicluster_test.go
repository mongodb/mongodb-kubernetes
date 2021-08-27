package multicluster

import (
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
