package cmd

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/filebrowser/filebrowser/v2/users"
)

func init() {
	usersCmd.AddCommand(usersImportCmd)
	usersImportCmd.Flags().Bool("overwrite", false, "overwrite users with the same id/username combo")
	usersImportCmd.Flags().Bool("replace", false, "replace the entire user base")
}

var usersImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import users from a file",
	Long: `Import users from a file. The path must be for a json or yaml
file. You can use this command to import new users to your
installation. For that, just don't place their ID on the files
list or set it to 0.`,
	Args: jsonYamlArg,
	RunE: withStore(func(cmd *cobra.Command, args []string, st *store) error {
		flags := cmd.Flags()
		list := []*users.User{}
		err := unmarshal(args[0], &list)
		if err != nil {
			return err
		}

		for _, user := range list {
			if user == nil {
				return errors.New("null user in import")
			}
			err = user.Clean("", false)
			if err != nil {
				return err
			}
		}

		replace, err := flags.GetBool("replace")
		if err != nil {
			return err
		}

		if replace {
			oldUsers, userImportErr := st.Users.Gets("", false)
			if userImportErr != nil {
				return userImportErr
			}

			err = marshal("users.backup.json", oldUsers)
			if err != nil {
				return err
			}
		}

		overwrite, err := flags.GetBool("overwrite")
		if err != nil {
			return err
		}

		importer, ok := st.Users.(interface {
			Import([]*users.User, bool, bool) error
		})
		if !ok {
			return errors.New("storage does not support atomic imports")
		}
		return importer.Import(list, replace, overwrite)
	}, storeOptions{}),
}
