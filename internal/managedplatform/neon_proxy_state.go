package managedplatform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
)

type NeonProxyRoleState struct {
	SCRAMSecret            string   `json:"scram_secret"`
	AllowedIPs             []string `json:"allowed_ips"`
	AllowedVPCEndpointIDs  []string `json:"allowed_vpc_endpoint_ids"`
	BlockPublicConnections bool     `json:"block_public_connections"`
	BlockVPCConnections    bool     `json:"block_vpc_connections"`
}

// NeonProxyBootstrapState contains only operator policy. The reconciler owns
// every route field and derives the globally unique endpoint ID from the
// managed platform ID.
type NeonProxyBootstrapState struct {
	EndpointID string                        `json:"endpoint_id"`
	Enabled    bool                          `json:"enabled"`
	Roles      map[string]NeonProxyRoleState `json:"roles"`
}

type NeonProxyEndpointState struct {
	EndpointID string                        `json:"endpoint_id"`
	Address    string                        `json:"address"`
	ServerName string                        `json:"server_name"`
	ProjectID  string                        `json:"project_id"`
	BranchID   string                        `json:"branch_id"`
	ComputeID  string                        `json:"compute_id"`
	Enabled    bool                          `json:"enabled"`
	Roles      map[string]NeonProxyRoleState `json:"roles"`
}

func SealNeonProxyRoles(key []byte, platformID string, revision int64, endpoint string, roles map[string]NeonProxyRoleState) ([]byte, error) {
	if len(key) != 32 || platformID == "" || revision < 1 || endpoint == "" || len(roles) == 0 || len(roles) > 64 {
		return nil, fmt.Errorf("Neon proxy role encryption input is invalid")
	}
	plain, err := json.Marshal(roles)
	if err != nil || len(plain) > 65500 {
		return nil, fmt.Errorf("Neon proxy role state is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad := []byte(fmt.Sprintf("hakopod-neon-proxy-v1:%s:%d:%s", platformID, revision, endpoint))
	return gcm.Seal(nonce, nonce, plain, aad), nil
}

func OpenNeonProxyRoles(key []byte, platformID string, revision int64, endpoint string, sealed []byte) (map[string]NeonProxyRoleState, error) {
	if len(key) != 32 || platformID == "" || revision < 1 || endpoint == "" {
		return nil, fmt.Errorf("Neon proxy role encryption input is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(sealed) < gcm.NonceSize()+gcm.Overhead() || len(sealed) > 65536 {
		return nil, fmt.Errorf("Neon proxy role state is invalid")
	}
	aad := []byte(fmt.Sprintf("hakopod-neon-proxy-v1:%s:%d:%s", platformID, revision, endpoint))
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], aad)
	if err != nil {
		return nil, fmt.Errorf("Neon proxy role state could not be authenticated")
	}
	var roles map[string]NeonProxyRoleState
	if err = json.Unmarshal(plain, &roles); err != nil || len(roles) == 0 || len(roles) > 64 {
		return nil, fmt.Errorf("Neon proxy role state is invalid")
	}
	return roles, nil
}
