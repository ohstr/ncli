package blossom

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipB7"
	"github.com/stretchr/testify/require"
)

func TestBuildAuthNamesEachServerHost(t *testing.T) {
	const priv = "0000000000000000000000000000000000000000000000000000000000000001"
	const pub = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

	ev, err := buildAuth(priv, pub, nipB7.VerbUpload, []string{"ab"}, time.Minute, "https://cdn.example.com", "http://relay:5500/")
	require.NoError(t, err)
	auth, err := nipB7.ParseAuthorization(ev)
	require.NoError(t, err)
	require.Equal(t, []string{"cdn.example.com", "relay:5500"}, auth.Servers)

	unscoped, err := buildAuth(priv, pub, nipB7.VerbList, nil, time.Minute)
	require.NoError(t, err)
	auth, err = nipB7.ParseAuthorization(unscoped)
	require.NoError(t, err)
	require.Empty(t, auth.Servers)
}
