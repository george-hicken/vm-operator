// © Broadcom. All Rights Reserved.
// The term “Broadcom” refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: Apache-2.0

package credentials

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// VSphereVMProviderCredentials wraps the data needed to login to vCenter.
type VSphereVMProviderCredentials struct {
	Username string
	Password string
}

// ErrSecretNotFound is returned, wrapped, when the credentials Secret does not
// exist. Unlike other failures it resolves on its own once the Secret is
// written, so callers can wait for it instead of backing off exponentially.
var ErrSecretNotFound = errors.New("provider credentials secret not found")

// GetProviderCredentials returns the vCenter credentials in the named Secret.
func GetProviderCredentials(
	ctx context.Context,
	client ctrlclient.Client,
	namespace, secretName string) (VSphereVMProviderCredentials, error) {

	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Namespace: namespace, Name: secretName}
	if err := client.Get(ctx, secretKey, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return VSphereVMProviderCredentials{}, fmt.Errorf(
				"cannot find secret for provider credentials: %s: %w: %w", secretKey, ErrSecretNotFound, err)
		}
		return VSphereVMProviderCredentials{}, fmt.Errorf("cannot find secret for provider credentials: %s: %w", secretKey, err)
	}

	return ExtractVCCredentials(secret.Data)
}

func ExtractVCCredentials(data map[string][]byte) (VSphereVMProviderCredentials, error) {
	credentials := VSphereVMProviderCredentials{
		Username: string(data["username"]),
		Password: string(data["password"]),
	}

	if credentials.Username == "" || credentials.Password == "" {
		return VSphereVMProviderCredentials{}, errors.New("vCenter username and password are missing")
	}

	return credentials, nil
}
