// Copyright Project Contour Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build e2e
// +build e2e

package httpproxy

import (
	"context"
	"fmt"
	"testing"

	contour_api_v1alpha1 "github.com/projectcontour/contour/apis/projectcontour/v1alpha1"

	"github.com/davecgh/go-spew/spew"
	certmanagerv1 "github.com/jetstack/cert-manager/pkg/apis/certmanager/v1"
	certmanagermetav1 "github.com/jetstack/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	contourv1 "github.com/projectcontour/contour/apis/projectcontour/v1"
	"github.com/projectcontour/contour/pkg/config"
	"github.com/projectcontour/contour/test/e2e"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var f = e2e.NewFramework(false)

func TestHTTPProxy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HTTPProxy tests")
}

var _ = BeforeSuite(func() {
	require.NoError(f.T(), f.Deployment.EnsureResourcesForLocalContour())
})

var _ = AfterSuite(func() {
	// Delete resources individually instead of deleting the entire contour
	// namespace as a performance optimization, because deleting non-empty
	// namespaces can take up to a couple minutes to complete.
	require.NoError(f.T(), f.Deployment.DeleteResourcesForLocalContour())
	gexec.CleanupBuildArtifacts()
})

var _ = Describe("HTTPProxy", func() {
	var (
		contourCmd            *gexec.Session
		contourConfig         *config.Parameters
		contourConfiguration  *contour_api_v1alpha1.ContourConfiguration
		contourConfigFile     string
		additionalContourArgs []string
	)

	BeforeEach(func() {
		// Contour config file contents, can be modified in nested
		// BeforeEach.
		contourConfig = &config.Parameters{}

		// Contour configuration crd, can be modified in nested
		// BeforeEach.
		contourConfiguration = e2e.DefaultContourConfiguration()

		// Default contour serve command line arguments can be appended to in
		// nested BeforeEach.
		additionalContourArgs = []string{}
	})

	// JustBeforeEach is called after each of the nested BeforeEach are
	// called, so it is a final setup step before running a test.
	// A nested BeforeEach may have modified Contour config, so we wait
	// until here to start Contour.
	JustBeforeEach(func() {
		var err error
		contourCmd, contourConfigFile, err = f.Deployment.StartLocalContour(contourConfig, contourConfiguration, additionalContourArgs...)
		require.NoError(f.T(), err)

		// Wait for Envoy to be healthy.
		require.NoError(f.T(), f.Deployment.WaitForEnvoyDaemonSetUpdated())
	})

	AfterEach(func() {
		require.NoError(f.T(), f.Deployment.StopLocalContour(contourCmd, contourConfigFile))
	})

	f.NamespacedTest("httpproxy-required-field-validation", testRequiredFieldValidation)

	f.NamespacedTest("httpproxy-header-condition-match", testHeaderConditionMatch)

	f.NamespacedTest("httpproxy-path-condition-match", testPathConditionMatch)

	f.NamespacedTest("httpproxy-https-sni-enforcement", testHTTPSSNIEnforcement)

	f.NamespacedTest("httpproxy-pod-restart", testPodRestart)

	f.NamespacedTest("httpproxy-merge-slash", testMergeSlash)

	f.NamespacedTest("httpproxy-client-cert-auth", testClientCertAuth)

	f.NamespacedTest("httpproxy-tcproute-https-termination", testTCPRouteHTTPSTermination)

	f.NamespacedTest("httpproxy-https-misdirected-request", testHTTPSMisdirectedRequest)

	f.NamespacedTest("httpproxy-include-prefix-condition", testIncludePrefixCondition)

	f.NamespacedTest("httpproxy-retry-policy-validation", testRetryPolicyValidation)

	f.NamespacedTest("httpproxy-invalid-wildcard-subdomain-fqdn", testWildcardSubdomainFQDN)
	f.NamespacedTest("httpproxy-invalid-wildcard-fqdn", testWildcardFQDN)

	f.NamespacedTest("httpproxy-https-fallback-certificate", func(namespace string) {
		Context("with fallback certificate", func() {
			BeforeEach(func() {
				contourConfig.TLS = config.TLSParameters{
					FallbackCertificate: config.NamespacedName{
						Name:      "fallback-cert",
						Namespace: namespace,
					},
				}
				contourConfiguration.Spec.HTTPProxy.FallbackCertificate = &contour_api_v1alpha1.NamespacedName{
					Name:      "fallback-cert",
					Namespace: namespace,
				}

				f.Certs.CreateSelfSignedCert(namespace, "fallback-cert", "fallback-cert", "fallback.projectcontour.io")
			})

			testHTTPSFallbackCertificate(namespace)
		})
	})

	f.NamespacedTest("httpproxy-backend-tls", func(namespace string) {
		Context("with backend tls", func() {
			BeforeEach(func() {
				// Top level issuer.
				selfSignedIssuer := &certmanagerv1.Issuer{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      "selfsigned",
					},
					Spec: certmanagerv1.IssuerSpec{
						IssuerConfig: certmanagerv1.IssuerConfig{
							SelfSigned: &certmanagerv1.SelfSignedIssuer{},
						},
					},
				}
				require.NoError(f.T(), f.Client.Create(context.TODO(), selfSignedIssuer))

				// CA to sign backend certs with.
				caCertificate := &certmanagerv1.Certificate{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      "ca-cert",
					},
					Spec: certmanagerv1.CertificateSpec{
						IsCA: true,
						Usages: []certmanagerv1.KeyUsage{
							certmanagerv1.UsageSigning,
							certmanagerv1.UsageCertSign,
						},
						CommonName: "ca-cert",
						SecretName: "ca-cert",
						IssuerRef: certmanagermetav1.ObjectReference{
							Name: "selfsigned",
						},
					},
				}
				require.NoError(f.T(), f.Client.Create(context.TODO(), caCertificate))

				// Issuer based on CA to generate new certs with.
				basedOnCAIssuer := &certmanagerv1.Issuer{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      "ca-issuer",
					},
					Spec: certmanagerv1.IssuerSpec{
						IssuerConfig: certmanagerv1.IssuerConfig{
							CA: &certmanagerv1.CAIssuer{
								SecretName: "ca-cert",
							},
						},
					},
				}
				require.NoError(f.T(), f.Client.Create(context.TODO(), basedOnCAIssuer))

				// Backend client cert, can use for upstream validation as well.
				backendClientCert := &certmanagerv1.Certificate{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      "backend-client-cert",
					},
					Spec: certmanagerv1.CertificateSpec{
						Usages: []certmanagerv1.KeyUsage{
							certmanagerv1.UsageClientAuth,
						},
						CommonName: "client",
						SecretName: "backend-client-cert",
						IssuerRef: certmanagermetav1.ObjectReference{
							Name: "ca-issuer",
						},
					},
				}
				require.NoError(f.T(), f.Client.Create(context.TODO(), backendClientCert))

				contourConfig.TLS = config.TLSParameters{
					ClientCertificate: config.NamespacedName{
						Namespace: namespace,
						Name:      "backend-client-cert",
					},
				}

				contourConfiguration.Spec.Envoy.ClientCertificate = &contour_api_v1alpha1.NamespacedName{
					Name:      "backend-client-cert",
					Namespace: namespace,
				}
			})

			testBackendTLS(namespace)
		})
	})

	f.NamespacedTest("httpproxy-external-auth", testExternalAuth)

	f.NamespacedTest("httpproxy-http-health-checks", testHTTPHealthChecks)

	f.NamespacedTest("httpproxy-dynamic-headers", testDynamicHeaders)

	f.NamespacedTest("httpproxy-host-header-rewrite", testHostHeaderRewrite)

	f.NamespacedTest("httpproxy-external-name-service-insecure", func(namespace string) {
		Context("with ExternalName Services enabled", func() {
			BeforeEach(func() {
				contourConfig.EnableExternalNameService = true
				contourConfiguration.Spec.EnableExternalNameService = true
			})
			testExternalNameServiceInsecure(namespace)
		})
	})

	f.NamespacedTest("httpproxy-external-name-service-tls", func(namespace string) {
		Context("with ExternalName Services enabled", func() {
			BeforeEach(func() {
				contourConfig.EnableExternalNameService = true
				contourConfiguration.Spec.EnableExternalNameService = true
			})
			testExternalNameServiceTLS(namespace)
		})
	})

	f.NamespacedTest("httpproxy-external-name-service-localhost", func(namespace string) {
		Context("with ExternalName Services enabled", func() {
			BeforeEach(func() {
				contourConfig.EnableExternalNameService = true
				contourConfiguration.Spec.EnableExternalNameService = true
			})
			testExternalNameServiceLocalhostInvalid(namespace)
		})
	})
	f.NamespacedTest("httpproxy-local-rate-limiting-vhost", testLocalRateLimitingVirtualHost)

	f.NamespacedTest("httpproxy-local-rate-limiting-route", testLocalRateLimitingRoute)

	Context("global rate limiting", func() {
		withRateLimitService := func(body e2e.NamespacedTestBody) e2e.NamespacedTestBody {
			return func(namespace string) {
				Context("with rate limit service", func() {
					BeforeEach(func() {
						contourConfig.RateLimitService = config.RateLimitService{
							ExtensionService: fmt.Sprintf("%s/%s", namespace, f.Deployment.RateLimitExtensionService.Name),
							Domain:           "contour",
							FailOpen:         false,
						}
						contourConfiguration.Spec.RateLimitService = &contour_api_v1alpha1.RateLimitServiceConfig{
							ExtensionService: contour_api_v1alpha1.NamespacedName{
								Name:      f.Deployment.RateLimitExtensionService.Name,
								Namespace: namespace,
							},
							Domain:                  "contour",
							FailOpen:                false,
							EnableXRateLimitHeaders: false,
						}
						require.NoError(f.T(),
							f.Deployment.EnsureRateLimitResources(
								namespace,
								`
domain: contour
descriptors:
  - key: generic_key
    value: vhostlimit
    rate_limit:
      unit: hour
      requests_per_unit: 1
  - key: route_limit_key
    value: routelimit
    rate_limit:
      unit: hour
      requests_per_unit: 1
  - key: generic_key
    value: tlsvhostlimit
    rate_limit:
      unit: hour
      requests_per_unit: 1
  - key: generic_key
    value: tlsroutelimit
    rate_limit:
      unit: hour
      requests_per_unit: 1`))
					})

					body(namespace)
				})
			}
		}

		f.NamespacedTest("httpproxy-global-rate-limiting-vhost-non-tls", withRateLimitService(testGlobalRateLimitingVirtualHostNonTLS))

		f.NamespacedTest("httpproxy-global-rate-limiting-route-non-tls", withRateLimitService(testGlobalRateLimitingRouteNonTLS))

		f.NamespacedTest("httpproxy-global-rate-limiting-vhost-tls", withRateLimitService(testGlobalRateLimitingVirtualHostTLS))

		f.NamespacedTest("httpproxy-global-rate-limiting-route-tls", withRateLimitService(testGlobalRateLimitingRouteTLS))
	})
})

// httpProxyValid returns true if the proxy has a .status.currentStatus
// of "valid".
func httpProxyValid(proxy *contourv1.HTTPProxy) bool {

	if proxy == nil {
		return false
	}

	if len(proxy.Status.Conditions) == 0 {
		return false
	}

	cond := proxy.Status.GetConditionFor("Valid")
	return cond.Status == "True"

}

// httpProxyErrors provides a pretty summary of any Errors on the HTTPProxy Valid condition.
// If there are no errors, the return value will be empty.
func httpProxyErrors(proxy *contourv1.HTTPProxy) string {
	cond := proxy.Status.GetConditionFor("Valid")
	errors := cond.Errors
	if len(errors) > 0 {
		return spew.Sdump(errors)
	}

	return ""
}