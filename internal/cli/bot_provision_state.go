package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/disgoorg/snowflake/v2"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

func readProvisionID(path string) (snowflake.ID, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read bot provision state: %w", err)
	}
	id, ok := voxdiscord.ParseSnowflake(string(data))
	if !ok {
		return 0, fmt.Errorf("invalid Discord ID in bot provision state %s", path)
	}
	return id, nil
}

func claimProvisionID(path string, id snowflake.ID) error {
	existing, err := readProvisionID(path)
	if err != nil {
		return err
	}
	if existing == id {
		return nil
	}
	if existing != 0 {
		return provisionBindingError(path, existing)
	}

	// Publish the completed file only if the binding is still absent. A competing
	// run sees either a complete binding or no binding, even if a process exits.
	file, err := os.CreateTemp(filepath.Dir(path), ".binding-*")
	if err != nil {
		return fmt.Errorf("create bot provision binding: %w", err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	_, writeErr := fmt.Fprintln(file, id)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write bot provision binding: %w", err)
	}

	err = os.Link(tempPath, path)
	if errors.Is(err, os.ErrExist) {
		existing, err := readProvisionID(path)
		if err != nil {
			return err
		}
		if existing != id {
			return provisionBindingError(path, existing)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("save bot provision binding: %w", err)
	}
	return nil
}

func provisionBindingError(path string, existing snowflake.ID) error {
	return output.UsageError(fmt.Sprintf("provision name is already bound to %s %s in this config directory", filepath.Base(path), existing))
}
