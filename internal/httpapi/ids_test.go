package httpapi

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestSecretIDMapBoundary(t *testing.T) {
	id := uuid.New()
	public, err := publicid.Encode(publicid.KindSecret, id)
	require.NoError(t, err)
	input := map[string]*string{"TOKEN": &public, "REMOVE": nil}
	internal, err := secretIDsFromPointer(&input)
	require.NoError(t, err)
	require.JSONEq(t, `{"TOKEN":"`+id.String()+`","REMOVE":null}`, string(internal))
	var output map[string]*string
	require.NoError(t, publicSecretIDs(internal, &output))
	require.Equal(t, input, output)
	plainInput := map[string]openapi.SecretID{"TOKEN": public}
	internal, err = secretIDsFromPointer(&plainInput)
	require.NoError(t, err)
	require.JSONEq(t, `{"TOKEN":"`+id.String()+`"}`, string(internal))
	var plainOutput map[string]openapi.SecretID
	require.NoError(t, publicSecretIDs(internal, &plainOutput))
	require.Equal(t, plainInput, plainOutput)
	for _, invalid := range []string{id.String(), "invalid", "sec_aaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		_, err := secretIDsFromPointer(&map[string]string{"TOKEN": invalid})
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		var responseError apierror.ResponseError
		require.ErrorAs(t, err, &responseError)
		require.Equal(t, http.StatusBadRequest, responseError.Status)
	}
	var absent *map[string]string
	internal, err = secretIDsFromPointer(absent)
	require.NoError(t, err)
	require.Nil(t, internal)
}
