package aws

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/openshift/hypershift/cmd/cluster/core"
	awsinfra "github.com/openshift/hypershift/cmd/infra/aws"
	"github.com/openshift/hypershift/cmd/log"
	"github.com/openshift/hypershift/cmd/util"
)

func NewDestroyCommand(opts *core.DestroyOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "aws",
		Short:        "Destroys a HostedCluster and its associated infrastructure on AWS",
		SilenceUsage: true,
	}

	opts.AWSPlatform = core.AWSPlatformDestroyOptions{
		AWSCredentialsFile: "",
		StsCredentialsFile: "",
		PreserveIAM:        false,
		Region:             "us-east-1",
	}

	cmd.Flags().StringVar(&opts.AWSPlatform.AWSCredentialsFile, "aws-creds", opts.AWSPlatform.AWSCredentialsFile, "Path to an AWS credentials file (required)")
	cmd.Flags().StringVar(&opts.AWSPlatform.RoleArn, "role-arn", opts.AWSPlatform.RoleArn, "The ARN of the role to assume when destroying the cluster.")
	cmd.Flags().StringVar(&opts.AWSPlatform.StsCredentialsFile, "sts-creds", opts.AWSPlatform.StsCredentialsFile, "Path to STS credentials file to use when assuming the role.")
	cmd.Flags().BoolVar(&opts.AWSPlatform.PreserveIAM, "preserve-iam", opts.AWSPlatform.PreserveIAM, "If true, skip deleting IAM. Otherwise destroy any default generated IAM along with other infra.")
	cmd.Flags().StringVar(&opts.AWSPlatform.Region, "region", opts.AWSPlatform.Region, "Cluster's region; inferred from the hosted cluster by default")
	cmd.Flags().StringVar(&opts.AWSPlatform.BaseDomain, "base-domain", opts.AWSPlatform.BaseDomain, "Cluster's base domain; inferred from the hosted cluster by default")
	cmd.Flags().StringVar(&opts.AWSPlatform.BaseDomainPrefix, "base-domain-prefix", opts.AWSPlatform.BaseDomainPrefix, "Cluster's base domain prefix; inferred from the hosted cluster by default")
	cmd.Flags().StringVar(&opts.CredentialSecretName, "secret-creds", opts.CredentialSecretName, "A Kubernetes secret with a platform credential, pull-secret and base-domain. The secret must exist in the supplied \"--namespace\"")
	cmd.Flags().DurationVar(&opts.AWSPlatform.AwsInfraGracePeriod, "aws-infra-grace-period", opts.AWSPlatform.AwsInfraGracePeriod, "Timeout for destroying infrastructure in minutes")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		err := ValidateCredentialInfo(opts)
		if err != nil {
			return err
		}

		if err = DestroyCluster(cmd.Context(), opts); err != nil {
			log.Log.Error(err, "Failed to destroy cluster")
			return err
		}

		return nil
	}

	return cmd
}

func destroyPlatformSpecifics(ctx context.Context, o *core.DestroyOptions) error {
	if o.AWSPlatform.PostDeleteAction != nil {
		o.AWSPlatform.PostDeleteAction()
	}
	infraID := o.InfraID
	baseDomain := o.AWSPlatform.BaseDomain
	baseDomainPrefix := o.AWSPlatform.BaseDomainPrefix
	region := o.AWSPlatform.Region

	// Override the credentialSecret with credentialFile
	var awsKeyID, awsSecretKey string
	var err error
	if len(o.AWSPlatform.AWSCredentialsFile) == 0 && len(o.CredentialSecretName) > 0 {
		_, awsKeyID, awsSecretKey, err = util.ExtractOptionsFromSecret(nil, o.CredentialSecretName, o.Namespace, "")
		if err != nil {
			return err
		}
	}
	o.Log.Info("Destroying infrastructure", "infraID", infraID)
	destroyInfraOpts := awsinfra.DestroyInfraOptions{
		Region:              region,
		InfraID:             infraID,
		AWSCredentialsFile:  o.AWSPlatform.AWSCredentialsFile,
		RoleArn:             o.AWSPlatform.RoleArn,
		StsCredentialsFile:  o.AWSPlatform.StsCredentialsFile,
		AWSKey:              awsKeyID,
		AWSSecretKey:        awsSecretKey,
		Name:                o.Name,
		BaseDomain:          baseDomain,
		BaseDomainPrefix:    baseDomainPrefix,
		AwsInfraGracePeriod: o.AWSPlatform.AwsInfraGracePeriod,
		Log:                 o.Log,
	}
	if err := destroyInfraOpts.Run(ctx); err != nil {
		return fmt.Errorf("failed to destroy infrastructure: %w", err)
	}

	if !o.AWSPlatform.PreserveIAM {
		o.Log.Info("Destroying IAM", "infraID", infraID)
		destroyOpts := awsinfra.DestroyIAMOptions{
			Region:             region,
			AWSCredentialsFile: o.AWSPlatform.AWSCredentialsFile,
			RoleArn:            o.AWSPlatform.RoleArn,
			StsCredentialsFile: o.AWSPlatform.StsCredentialsFile,
			AWSKey:             awsKeyID,
			AWSSecretKey:       awsSecretKey,
			InfraID:            infraID,
			Log:                o.Log,
		}
		if err := destroyOpts.Run(ctx); err != nil {
			return fmt.Errorf("failed to destroy IAM: %w", err)
		}
	}
	return nil
}

func DestroyCluster(ctx context.Context, o *core.DestroyOptions) error {
	hostedCluster, err := core.GetCluster(ctx, o)
	if err != nil {
		return err
	}
	if hostedCluster != nil {
		o.InfraID = hostedCluster.Spec.InfraID
		o.AWSPlatform.Region = hostedCluster.Spec.Platform.AWS.Region
		o.AWSPlatform.BaseDomain = hostedCluster.Spec.DNS.BaseDomain

		if hostedCluster.Spec.DNS.BaseDomainPrefix != nil {
			if *hostedCluster.Spec.DNS.BaseDomainPrefix == "" {
				o.AWSPlatform.BaseDomainPrefix = "none"
			} else {
				o.AWSPlatform.BaseDomainPrefix = *hostedCluster.Spec.DNS.BaseDomainPrefix
			}
		}
	}

	var inputErrors []error
	if len(o.InfraID) == 0 {
		inputErrors = append(inputErrors, fmt.Errorf("infrastructure ID is required"))
	}
	if len(o.AWSPlatform.BaseDomain) == 0 {
		inputErrors = append(inputErrors, fmt.Errorf("base domain is required"))
	}
	if len(o.AWSPlatform.Region) == 0 {
		inputErrors = append(inputErrors, fmt.Errorf("region is required"))
	}
	if err := errors.NewAggregate(inputErrors); err != nil {
		return fmt.Errorf("required inputs are missing: %w", err)
	}

	return core.DestroyCluster(ctx, hostedCluster, o, destroyPlatformSpecifics)
}

// ValidateCredentialInfo validates if the credentials secret name is empty, the aws-creds or sts-creds mutually exclusive and are not empty; validates if
// the credentials secret is not empty, that it can be retrieved.
func ValidateCredentialInfo(opts *core.DestroyOptions) error {
	if len(opts.CredentialSecretName) == 0 {
		if opts.AWSPlatform.AWSCredentialsFile == "" {
			if err := util.IsRequiredOption("role-arn", opts.AWSPlatform.RoleArn); err != nil {
				return err
			}
			if err := util.IsRequiredOption("sts-creds", opts.AWSPlatform.StsCredentialsFile); err != nil {
				return err
			}
		}
		if opts.AWSPlatform.RoleArn == "" && opts.AWSPlatform.StsCredentialsFile == "" {
			if err := util.IsRequiredOption("aws-creds", opts.AWSPlatform.AWSCredentialsFile); err != nil {
				return err
			}
		}
		if opts.AWSPlatform.StsCredentialsFile != "" && opts.AWSPlatform.AWSCredentialsFile != "" {
			return fmt.Errorf("only one of 'aws-creds' or 'role-arn' and 'sts-creds' can be provided")
		}
	} else {
		// Check the secret exists now, otherwise stop
		opts.Log.Info("Retrieving credentials secret", "namespace", opts.Namespace, "name", opts.CredentialSecretName)
		if _, err := util.GetSecret(opts.CredentialSecretName, opts.Namespace); err != nil {
			return err
		}
	}

	return nil
}
