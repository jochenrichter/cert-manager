/*
Copyright 2020 The cert-manager Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package secret

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	cmmeta "github.com/jetstack/cert-manager/pkg/apis/meta/v1"
	"github.com/jetstack/cert-manager/pkg/util/pki"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/util/i18n"
	"k8s.io/kubectl/pkg/util/templates"
	k8sclock "k8s.io/utils/clock"
)

var clock k8sclock.Clock = k8sclock.RealClock{}

const validForTemplate = `Valid for:
	DNS Names: %s
	URIs: %s
	IP Addresses: %s
	Email Addresses: %s
	Usages: %s`

const validityPeriodTemplate = `Validity period:
	Not Before: %s
	Not After: %s`

const issuedByTemplate = `Issued By:
	Common Name		%s
	Organization		%s
	OrganizationalUnit	%s
	Country: 		%s`

const issuedForTemplate = `Issued For:
	Common Name		%s
	Organization		%s
	OrganizationalUnit	%s
	Country: 		%s`

const certificateTemplate = `Certificate:
	Signing Algorithm:	%s
	Public Key Algorithm: 	%s
	Serial Number:	%s
	Fingerprints: 	%s
	Is a CA certificate: %v
	CRL:	%s
	OCSP:	%s`

const debuggingTemplate = `Debugging:
	Trusted by this computer:	%s
	CRL Status:	%s
	OCSP Status:	%s`

var (
	long = templates.LongDesc(i18n.T(`
Get details about a kubernetes.io/tls typed secret`))

	example = templates.Examples(i18n.T(`
# Query information about a secret with name 'my-crt' in namespace 'my-namespace'
kubectl cert-manager inspect secret my-crt --namespace my-namespace
`))
)

// Options is a struct to support status certificate command
type Options struct {
	RESTConfig *restclient.Config
	// The Namespace that the Certificate to be queried about resides in.
	// This flag registration is handled by cmdutil.Factory
	Namespace string

	clientSet *kubernetes.Clientset

	genericclioptions.IOStreams
}

// NewOptions returns initialized Options
func NewOptions(ioStreams genericclioptions.IOStreams) *Options {
	return &Options{
		IOStreams: ioStreams,
	}
}

// NewCmdInspectSecret returns a cobra command for status certificate
func NewCmdInspectSecret(ioStreams genericclioptions.IOStreams, factory cmdutil.Factory) *cobra.Command {
	o := NewOptions(ioStreams)
	cmd := &cobra.Command{
		Use:     "secret",
		Short:   "Get details about a kubernetes.io/tls typed secret",
		Long:    long,
		Example: example,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Validate(args))
			cmdutil.CheckErr(o.Complete(factory))
			cmdutil.CheckErr(o.Run(args))
		},
	}
	return cmd
}

// Validate validates the provided options
func (o *Options) Validate(args []string) error {
	if len(args) < 1 {
		return errors.New("the name of the Secret has to be provided as argument")
	}
	if len(args) > 1 {
		return errors.New("only one argument can be passed in: the name of the Secret")
	}
	return nil
}

// Complete takes the factory and infers any remaining options.
func (o *Options) Complete(f cmdutil.Factory) error {
	var err error

	o.Namespace, _, err = f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.RESTConfig, err = f.ToRESTConfig()
	if err != nil {
		return err
	}

	o.clientSet, err = kubernetes.NewForConfig(o.RESTConfig)
	if err != nil {
		return err
	}

	return nil
}

// Run executes status certificate command
func (o *Options) Run(args []string) error {
	ctx := context.TODO()

	secret, err := o.clientSet.CoreV1().Secrets(o.Namespace).Get(ctx, args[0], metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error when finding Secret %q: %w\n", args[0], err)
	}

	certData := secret.Data[corev1.TLSCertKey]
	certs, err := splitPEMs(certData)
	if err != nil {
		return err
	}
	if len(certs) < 1 {
		return errors.New("no PEM data found in secret")
	}

	intermediates := [][]byte(nil)
	if len(certs) > 1 {
		intermediates = certs[1:]
	}

	// we only want to inspect the leaf certificate
	x509Cert, err := pki.DecodeX509CertificateBytes(certs[0])
	if err != nil {
		return fmt.Errorf("error when parsing 'tls.crt': %w", err)
	}

	out := []string{
		describeValidFor(x509Cert),
		describeValidityPeriod(x509Cert),
		describeIssuedBy(x509Cert),
		describeIssuedFor(x509Cert),
		describeCertificate(x509Cert),
		describeDebugging(x509Cert, intermediates, secret.Data[cmmeta.TLSCAKey]),
	}

	fmt.Println(strings.Join(out, "\n\n"))

	return nil
}

func describeValidFor(cert *x509.Certificate) string {
	return fmt.Sprintf(validForTemplate,
		printSlice(cert.DNSNames),
		printSlice(pki.URLsToString(cert.URIs)),
		printSlice(pki.IPAddressesToString(cert.IPAddresses)),
		printSlice(cert.EmailAddresses),
		printKeyUsage(pki.BuildCertManagerKeyUsages(cert.KeyUsage, cert.ExtKeyUsage)),
	)
}

func describeValidityPeriod(cert *x509.Certificate) string {
	return fmt.Sprintf(validityPeriodTemplate,
		cert.NotBefore.Format(time.RFC1123),
		cert.NotAfter.Format(time.RFC1123),
	)
}

func describeIssuedBy(cert *x509.Certificate) string {
	return fmt.Sprintf(issuedByTemplate,
		printOrNone(cert.Issuer.CommonName),
		printSliceOrOne(cert.Issuer.Organization),
		printSliceOrOne(cert.Issuer.OrganizationalUnit),
		printSliceOrOne(cert.Issuer.Country),
	)
}

func describeIssuedFor(cert *x509.Certificate) string {
	return fmt.Sprintf(issuedForTemplate,
		printOrNone(cert.Subject.CommonName),
		printSliceOrOne(cert.Subject.Organization),
		printSliceOrOne(cert.Subject.OrganizationalUnit),
		printSliceOrOne(cert.Subject.Country),
	)
}

func describeCertificate(cert *x509.Certificate) string {
	return fmt.Sprintf(certificateTemplate,
		cert.SignatureAlgorithm.String(),
		cert.PublicKeyAlgorithm.String(),
		cert.SerialNumber.String(),
		fingerprintCert(cert),
		cert.IsCA,
		printSliceOrOne(cert.CRLDistributionPoints),
		printSliceOrOne(cert.OCSPServer),
	)
}

func describeDebugging(cert *x509.Certificate, intermediates [][]byte, ca []byte) string {
	return fmt.Sprintf(debuggingTemplate,
		describeTrusted(cert, intermediates),
		describeCRL(cert),
		describeOCSP(cert, intermediates, ca),
	)
}

func describeCRL(cert *x509.Certificate) string {
	if len(cert.CRLDistributionPoints) < 1 {
		return "No CRL endpoints set"
	}

	hasChecked := false
	for _, crlURL := range cert.CRLDistributionPoints {
		u, err := url.Parse(crlURL)
		if err != nil {
			continue // not a valid URL
		}
		if u.Scheme != "ldap" && u.Scheme != "https" {
			continue
		}

		hasChecked = true
		valid, err := checkCRLValidCert(cert, crlURL)
		if err != nil {
			return fmt.Sprintf("Cannot check CRL: %s", err.Error())
		}
		if !valid {
			return fmt.Sprintf("Revoked by %s", crlURL)
		}
	}

	if !hasChecked {
		return "No CRL endpoints we support found"
	}

	return "Valid"
}

func describeOCSP(cert *x509.Certificate, intermediates [][]byte, ca []byte) string {
	if len(ca) > 1 {
		intermediates = append([][]byte{ca}, intermediates...)
	}
	if len(intermediates) < 1 {
		return "Cannot check OCSP, does not have a CA or intermediate certificate provided"
	}
	issuerCert, err := pki.DecodeX509CertificateBytes(intermediates[len(intermediates)-1])
	if err != nil {
		return fmt.Sprintf("Cannot parse intermediate certificate: %s", err.Error())
	}

	valid, err := checkOCSPValidCert(cert, issuerCert)
	if err != nil {
		return fmt.Sprintf("Cannot check OCSP: %s", err.Error())
	}

	if !valid {
		return "Marked as revoked"
	}

	return "valid"
}

func describeTrusted(cert *x509.Certificate, intermediates [][]byte) string {
	systemPool, err := x509.SystemCertPool()
	if err != nil {
		return fmt.Sprintf("Error getting system CA store: %s", err.Error())
	}
	for _, intermediate := range intermediates {
		systemPool.AppendCertsFromPEM(intermediate)
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:       systemPool,
		CurrentTime: clock.Now(),
	})
	if err == nil {
		return "yes"
	}
	return fmt.Sprintf("no: %s", err.Error())
}
