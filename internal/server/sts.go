package server

import (
	"encoding/xml"
	"net/http"
)

const stsNamespace = "https://sts.amazonaws.com/doc/2011-06-15/"

type stsGetCallerIdentityResponse struct {
	XMLName xml.Name                   `xml:"GetCallerIdentityResponse"`
	XMLNS   string                     `xml:"xmlns,attr"`
	Result  stsGetCallerIdentityResult `xml:"GetCallerIdentityResult"`
	Meta    stsResponseMetadata        `xml:"ResponseMetadata"`
}

type stsGetCallerIdentityResult struct {
	Arn     string `xml:"Arn"`
	UserID  string `xml:"UserId"`
	Account string `xml:"Account"`
}

type stsResponseMetadata struct {
	RequestID string `xml:"RequestId"`
}

func (s *Server) handleSTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || r.Form.Get("Action") != "GetCallerIdentity" || r.Form.Get("Version") != "2011-06-15" {
		writeSTSError(w, "InvalidAction", "The action is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(stsGetCallerIdentityResponse{
		XMLNS: stsNamespace,
		Result: stsGetCallerIdentityResult{
			Arn: "arn:aws:iam::000000000000:user/fcp-local", UserID: "AIDAFCPLOCAL000000000", Account: "000000000000",
		},
		Meta: stsResponseMetadata{RequestID: requestID()},
	})
}

func writeSTSError(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`<?xml version="1.0"?><ErrorResponse xmlns="` + stsNamespace + `"><Error><Type>Sender</Type><Code>` + code + `</Code><Message>` + message + `</Message></Error><RequestId>` + requestID() + `</RequestId></ErrorResponse>`))
}
