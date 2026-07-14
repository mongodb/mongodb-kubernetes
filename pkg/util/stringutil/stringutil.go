package stringutil

import (
	"net/url"
	"strings"
)

// Ref is a convenience function which returns
// a reference to the provided string
func Ref(s string) *string {
	return &s
}

// Contains returns true if there is at least one string in `slice`
// that is equal to `s`.
func Contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// CheckCertificateAddresses determines if the provided FQDN can match any of the addresses or
// SubjectAltNames (SAN) in an array of FQDNs/wildcards/shortnames.
// Both the availableAddressNames and the testAddressName can contain wildcards, e.g. *.cluster-1.example.com
// Once a wildcard is found on any tested argument, only a domain-level comparison is checked.
func CheckCertificateAddresses(availableAddressNames []string, testAddressName string) bool {
	checkedTestAddressName := CheckWithLevelDomain(testAddressName)
	star := "*"
	for _, availableAddress := range availableAddressNames {
		// Determine if the certificate name is a wildcard, FQDN, unqualified domain name, or shortname
		// Strip the first character from the wildcard and hostname to determine if they match
		// (wildcards only work for one level of domain)
		if availableAddress[0:1] == star {
			checkAddress := CheckWithLevelDomain(availableAddress)
			if checkAddress == checkedTestAddressName {
				return true
			}
		}
		if availableAddress == testAddressName {
			return true
		}
		// This is the multi-cluster with an external domain case.
		// We do not want to deal if this is per-member cert or a wildcard, that's why we will only
		// compare the domains.
		if testAddressName[0:1] == star {
			domainOnlyTestAddress := CheckWithLevelDomain(testAddressName)
			domainOnlyAvailableAddress := CheckWithLevelDomain(availableAddress)

			if domainOnlyAvailableAddress == domainOnlyTestAddress {
				return true
			}
		}
	}
	return false
}

// CheckWithLevelDomain determines if the address is a shortname/top level domain
// or FQDN/Unqualified Domain Name
func CheckWithLevelDomain(address string) string {
	addressExploded := strings.Split(address, ".")
	if len(addressExploded) < 2 {
		return addressExploded[0]
	}
	return strings.Join(addressExploded[1:], ".")
}

func ContainsAny(slice []string, ss ...string) bool {
	for _, s := range ss {
		if Contains(slice, s) {
			return true
		}
	}

	return false
}

func Remove(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return result
}

// EncodeUserinfoComponent percent-encodes a username or password for use in a
// MongoDB connection string. Space is encoded as %20 (not +) so that pymongo's
// unquote_plus and the Go driver both decode it correctly, and + is encoded as
// %2B so pymongo does not decode it as a space.
// https://github.com/mongodb/mongo-python-driver/blob/master/pymongo/uri_parser_shared.py#L146
func EncodeUserinfoComponent(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// UpperCaseFirstChar ensures the message first char is uppercased.
func UpperCaseFirstChar(msg string) string {
	if msg == "" {
		return ""
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}
