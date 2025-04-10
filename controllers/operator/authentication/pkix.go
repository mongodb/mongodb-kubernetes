package authentication

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/xerrors"
)

type encodeState struct {
	bytes.Buffer // accumulated output
}

const (
	commonNameOID           = "2.5.4.3"
	serialNumberOID         = "2.5.4.5"
	countryNameOID          = "2.5.4.6"
	localityNameOID         = "2.5.4.7"
	stateOrProvinceNameOID  = "2.5.4.8"
	streetAddressOID        = "2.5.4.9"
	organizationNameOID     = "2.5.4.10"
	organizationUnitNameOID = "2.5.4.11"
	postalCodeOID           = "2.5.4.17"
	useridOID               = "0.9.2342.19200300.100.1.1"
	domainComponentOID      = "0.9.2342.19200300.100.1.25"
	emailAddressOID         = "1.2.840.113549.1.9.1"
	subjectAltNameOID       = "2.5.29.17"
	businessCategoryOID     = "2.5.4.15"
)

var shortNamesByOID = map[string]string{
	commonNameOID:           "CN",
	serialNumberOID:         "serialNumber",
	countryNameOID:          "C",
	localityNameOID:         "L",
	stateOrProvinceNameOID:  "ST",
	streetAddressOID:        "street",
	organizationNameOID:     "O",
	organizationUnitNameOID: "OU",
	postalCodeOID:           "postalCode",
	useridOID:               "UID",
	domainComponentOID:      "DC",
	emailAddressOID:         "emailAddress",
	subjectAltNameOID:       "subjectAltName",
	businessCategoryOID:     "businessCategory",
}

func GetCertificateSubject(certPEM string) (subject string, unknownOIDs []string, err error) {
	rem := []byte(certPEM)
	block, _ := pem.Decode(rem)
	if block == nil {
		return "", []string{}, xerrors.Errorf("no certificate found")
	}

	x509Cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", []string{}, err
	}

	var subjectRDN pkix.RDNSequence
	if _, err := asn1.Unmarshal(x509Cert.RawSubject, &subjectRDN); err != nil {
		return "", []string{}, err
	}

	var enc encodeState
	unknownOIDs, err = enc.writeDistinguishedName(subjectRDN)
	if err != nil {
		return "", []string{}, err
	}
	return enc.String(), unknownOIDs, nil
}

func (enc *encodeState) writeDistinguishedName(subject pkix.RDNSequence) (allUnknownOIDs []string, err error) {
	// Section 2.1. Converting the RDNSequence
	//
	// If the RDNSequence is an empty sequence, the result is the empty or
	// zero length string.
	//
	// Otherwise, the output consists of the string encodings of each
	// RelativeDistinguishedName in the RDNSequence (according to 2.2),
	// starting with the last element of the sequence and moving backwards
	// toward the first.
	//
	// The encodings of adjoining RelativeDistinguishedNames are separated
	// by a comma character (',' ASCII 44).

	allUnknownOIDs = make([]string, 0)

	for i := len(subject) - 1; i >= 0; i-- {
		if i < len(subject)-1 {
			enc.WriteByte(',')
		}

		unknownOIDs, err := enc.writeRelativeDistinguishedName(subject[i])
		if err != nil {
			return []string{}, err
		}
		allUnknownOIDs = append(allUnknownOIDs, unknownOIDs...)
	}
	return allUnknownOIDs, nil
}

func (enc *encodeState) writeRelativeDistinguishedName(rdn pkix.RelativeDistinguishedNameSET) (unknownOIDs []string, err error) {
	if len(rdn) == 0 {
		return []string{}, errors.New("expected RelativeDistinguishedNameSET to contain at least 1 attribute")
	}

	// 2.2. Converting RelativeDistinguishedName
	//
	// When converting from an ASN.1 RelativeDistinguishedName to a string,
	// the output consists of the string encodings of each
	// AttributeTypeAndValue (according to 2.3), in any order.
	//
	// Where there is a multi-valued RDN, the outputs from adjoining
	// AttributeTypeAndValues are separated by a plus ('+' ASCII 43)
	// character.

	// TODO: This does not conform to the same order of attributes that OpenSSL uses

	unknownOIDs = make([]string, 0)
	for i := 0; i < len(rdn); i++ {
		if i > 0 {
			enc.WriteByte('+')
		}

		found, err := enc.writeAttributeTypeAndValue(rdn[i])
		if err != nil {
			return []string{}, err
		}
		if !found {
			unknownOIDs = append(unknownOIDs, rdn[i].Type.String())
		}
	}
	return unknownOIDs, nil
}

func (enc *encodeState) getAttributeTypeShortName(attrType asn1.ObjectIdentifier) (shortName string, found bool) {
	oid := attrType.String()
	shortName, found = shortNamesByOID[oid]
	if found {
		return shortName, true
	}

	return "", false
}

func (enc *encodeState) writeAttributeTypeAndValue(atv pkix.AttributeTypeAndValue) (found bool, err error) {
	// Section 2.3. Converting AttributeTypeAndValue
	//
	// The AttributeTypeAndValue is encoded as the string representation of
	// the AttributeType, followed by an equals character ('=' ASCII 61),
	// followed by the string representation of the AttributeValue.

	attrType, found := enc.getAttributeTypeShortName(atv.Type)
	if err != nil {
		// Technically, the AttributeType should be encoded using the dotted-decimal
		// notation of its object identifier (OID); however, chances are good that
		// it is exists in OpenSSL's list, so our authentication attempt would get
		// rejected by the server anyway.
		return false, err
	}

	attrValue, ok := atv.Value.(string)
	if !ok {
		// Technically, the AttributeValue should be encoded as an octothorpe
		// character ('#' ASCII 35) followed by the hexadecimal representation
		// of the bytes from the BER encoding. However, there is no need to
		// handle that case because all of the recognized attributes are of type
		// IA5String, PrintableString, or UTF8String.
		return false, xerrors.Errorf("value for attribute type `%v` was not a string: %v", atv.Type, atv.Value)
	}

	if found {
		enc.WriteString(attrType)
	} else {
		enc.WriteString(atv.Type.String())
	}
	enc.WriteByte('=')
	err = enc.writeEscapedAttributeValue(attrValue)
	if err != nil {
		return false, err
	}
	return found, nil
}

func (enc *encodeState) writeEscapedAttributeValue(attrValue string) error {
	// Section 2.4 Converting an AttributeValue from ASN.1 to a String
	//
	// If the UTF-8 string does not have any of the following characters
	// which need escaping, then that string can be used as the string
	// representation of the value.
	//
	//   - a space or "#" character occurring at the beginning of the string
	//   - a space character occurring at the end of the string
	//   - one of the characters ",", "+", """, "\", "<", ">" or ";"
	//
	// If a character to be escaped is one of the list shown above, then it
	// is prefixed by a backslash ('\' ASCII 92).
	//
	// Otherwise the character to be escaped is replaced by a backslash and
	// two hex digits, which form a single byte in the code of the character.

	start := 0
	for i := 0; i < len(attrValue); {
		b := attrValue[i]
		// OpenSSL does not actually escape NUL as \00, as it only became required
		// to do so in RFC 4514. Instead, the attribute value is terminated before
		// the end of line character. For example, the inputs "CN=ab" and "CN=ab\x00c"
		// both produce the same certificate request. This should mean that no null
		// characters can appear in the UTF-8 string, so our handling of them here
		// is irrelevant - but not incorrect.
		if b < 0x20 || b == 0x7f || b >= utf8.RuneSelf {
			if start < i {
				enc.WriteString(attrValue[start:i])
			}
			enc.WriteByte('\\')
			hexDigits := hex.EncodeToString([]byte{b})
			// OpenSSL uses uppercase hexadecimal digits.
			enc.WriteString(strings.ToUpper(hexDigits))
			i++
			start = i
			continue
		}

		switch b {
		case ',', '+', '"', '\\', '<', '>', ';':
			if start < i {
				enc.WriteString(attrValue[start:i])
			}
			enc.WriteByte('\\')
			enc.WriteByte(b)
			i++
			start = i
			continue
		}

		if i == 0 && (b == ' ' || b == '#') {
			// OpenSSL only escapes the first space or hash character, not all leading ones.
			enc.WriteByte('\\')
			enc.WriteByte(b)
			i++
			start = i
		} else if i == len(attrValue)-1 && b == ' ' {
			// OpenSSL only escapes the last space character, not all trailing ones.
			if start < i {
				enc.WriteString(attrValue[start:i])
			}
			enc.WriteByte('\\')
			enc.WriteByte(b)
			i++
			start = i
		} else {
			i++
		}
	}
	if start < len(attrValue) {
		enc.WriteString(attrValue[start:])
	}
	return nil
}
