package main

import (
	"fmt"
	"github.com/aws/aws-sdk-go/service/sso"
	"github.com/aws/aws-sdk-go/service/sso/ssoiface"
	"github.com/aws/aws-sdk-go/service/ssooidc/ssooidciface"
	. "github.com/theurichde/go-aws-sso/internal"
	"github.com/urfave/cli/v2"
	"github.com/urfave/cli/v2/altsrc"
	"log"
	"os"
	"strings"
	"time"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = time.Now().String()
)

func main() {

	initialFlags := []cli.Flag{
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "start-url",
			Aliases: []string{"u"},
			Usage:   "set / override the SSO login start-url. (Example: https://my-login.awsapps.com/start#/)",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "region",
			Aliases: []string{"r"},
			Usage:   "set / override the AWS region",
		}),
		altsrc.NewStringFlag(&cli.StringFlag{
			Name:    "profile",
			Aliases: []string{"p"},
			Value:   "default",
			Usage:   "the profile name you want to set in your ~/.aws/credentials file",
		}),
		altsrc.NewBoolFlag(&cli.BoolFlag{
			Name:  "persist",
			Usage: "whether or not you want to write your short-living credentials to ~/.aws/credentials",
		}),
		&cli.BoolFlag{
			Name:     "force",
			Usage:    "removes the temporary access token and forces the retrieval of a new token",
			Value:    false,
			Hidden:   false,
			Required: false,
		},
	}

	cli.VersionPrinter = func(c *cli.Context) {
		fmt.Printf("Version: %s\nCommit: %s\nBuild Time: %s\n", version, commit, date)
	}

	commands := []*cli.Command{
		{
			Name:  "config",
			Usage: "Handles configuration. Note: Config location defaults to ${HOME}/.aws/go-aws-sso-config.yaml",
			Subcommands: []*cli.Command{
				{
					Name:        "generate",
					Usage:       "Generate a config file",
					Description: "Generates a config file. All available properties are interactively prompted.\nOverrides the existing config file!",
					Action:      GenerateConfigAction,
				},
				{
					Name:        "edit",
					Usage:       "Edit the config file",
					Description: "Edit the config file. All available properties are interactively prompted.\nOverrides the existing config file!",
					Action:      EditConfigAction,
				},
			},
		},
		{
			Name:        "refresh",
			Usage:       "Refresh your previously used credentials.",
			Description: "Refreshes the short living credentials based on your last account and role.",
			Action: func(context *cli.Context) error {
				checkMandatoryFlags(context)
				applyForceFlag(context)
				oidcApi, ssoApi := InitClients(context.String("region"))
				RefreshCredentials(oidcApi, ssoApi, context)
				return nil
			},
			Before: ReadConfigFile(initialFlags),
			Flags:  initialFlags,
		},
		{
			Name:        "assume",
			Usage:       "Assume directly into an account and SSO role",
			Description: "Assume directly into an account and SSO role",
			Action: func(context *cli.Context) error {
				checkMandatoryFlags(context)
				applyForceFlag(context)
				oidcApi, ssoApi := InitClients(context.String("region"))
				AssumeDirectly(oidcApi, ssoApi, context)
				return nil
			},
			Before: ReadConfigFile(initialFlags),
			Flags: append(initialFlags, []cli.Flag{
				altsrc.NewStringFlag(&cli.StringFlag{
					Name:    "role-name",
					Aliases: []string{"n"},
					Usage:   "The role name you want to assume",
				}),
				altsrc.NewStringFlag(&cli.StringFlag{
					Name:    "account-id",
					Aliases: []string{"a"},
					Usage:   "The account id where your role lives in",
				}),
			}...),
		},
	}

	app := &cli.App{
		Name:                 "go-aws-sso",
		Usage:                "Retrieve short-living credentials via AWS SSO & SSOOIDC",
		EnableBashCompletion: true,
		Action: func(context *cli.Context) error {

			if len(context.Args().Slice()) != 0 {
				fmt.Printf("Command not found: %s\n", context.Args().First())
				println("Try help or --help for usage")
				os.Exit(1)
			}

			checkMandatoryFlags(context)

			oidcApi, ssoApi := InitClients(context.String("region"))
			applyForceFlag(context)
			start(oidcApi, ssoApi, context, Prompter{})
			return nil
		},
		Flags:    initialFlags,
		Commands: commands,
		Before:   ReadConfigFile(initialFlags),
		Version:  version,
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func ReadConfigFile(flags []cli.Flag) cli.BeforeFunc {
	return func(context *cli.Context) error {
		inputSource, err := altsrc.NewYamlSourceFromFile(ConfigFilePath())
		if err != nil {
			if strings.Contains(err.Error(), "because it does not exist.") {
				return nil
			}
		}
		if err != nil {
			return fmt.Errorf("Unable to create input source: inner error: \n'%v'", err.Error())
		}

		return altsrc.ApplyInputSourceValues(context, inputSource, flags)
	}
}

func start(oidcClient ssooidciface.SSOOIDCAPI, ssoClient ssoiface.SSOAPI, context *cli.Context, promptSelector Prompt) {

	startUrl := context.String("start-url")
	clientInformation, _ := ProcessClientInformation(oidcClient, startUrl)

	accountInfo := RetrieveAccountInfo(clientInformation, ssoClient, promptSelector)
	roleInfo := RetrieveRoleInfo(accountInfo, clientInformation, ssoClient, promptSelector)
	SaveUsageInformation(accountInfo, roleInfo)

	rci := &sso.GetRoleCredentialsInput{AccountId: accountInfo.AccountId, RoleName: roleInfo.RoleName, AccessToken: &clientInformation.AccessToken}
	roleCredentials, err := ssoClient.GetRoleCredentials(rci)
	check(err)

	if context.Bool("persist") {
		template := ProcessPersistedCredentialsTemplate(roleCredentials, context.String("profile"))
		WriteAWSCredentialsFile(template)
		log.Printf("Credentials expire at: %s\n", time.Unix(*roleCredentials.RoleCredentials.Expiration/1000, 0))
	} else {
		template := ProcessCredentialProcessTemplate(*accountInfo.AccountId, *roleInfo.RoleName, context.String("profile"), context.String("region"))
		WriteAWSCredentialsFile(template)
	}

}

func check(err error) {
	if err != nil {
		log.Fatalf("Something went wrong: %q", err)
	}
}

func checkMandatoryFlags(context *cli.Context) {
	if context.String("start-url") == "" || context.String("region") == "" {
		log.Println("No Start URL given. Please set it now.")
		err := GenerateConfigAction(context)
		check(err)
		appConfig := ReadConfig(ConfigFilePath())
		err = context.Set("start-url", appConfig.StartUrl)
		check(err)
		err = context.Set("region", appConfig.Region)
		check(err)
	}
}

func applyForceFlag(context *cli.Context) {
	if context.Bool("force") {
		err := os.Remove(ClientInfoFileDestination())
		if err != nil {
			log.Printf("Nothing to do, temporary acces token found")
		}
		log.Printf("Successful removed temporary acces token")
	}
}
