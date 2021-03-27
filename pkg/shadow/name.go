package shadow

import (
	"fmt"
	"hash/fnv"
)

// Name returns the shadow service name corresponding to the given service namespace and name.
func Name(namespace, name string) (string, error) {
	hash := fnv.New128a()

	_, err := hash.Write([]byte(namespace + name))
	if err != nil {
		return "", fmt.Errorf("unable to hash service namespace and name: %w", err)
	}

	return fmt.Sprintf("shadow-svc-%x", hash.Sum(nil)), nil
}
