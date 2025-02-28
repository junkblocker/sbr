package types

import (
	"encoding/xml"
)

type (
	PhoneNumber    string
	SMSMessageType int
	SMSStatus      int
	AndroidTS      string
	BoolValue      int
	ReadStatus     int
	CallType       int
)

type SMS struct {
	XMLName xml.Name    `xml:"sms"`
	Address PhoneNumber `xml:"address,attr"`
	Body    string      `xml:"body,attr"`
	Date    string      `xml:"date,attr"`
}

type MMS struct {
	XMLName           xml.Name      `xml:"mms"`
	TextOnly          BoolValue     `xml:"text_only,attr"`
	Read              ReadStatus    `xml:"read,attr"`
	Date              string        `xml:"date,attr"`
	Locked            BoolValue     `xml:"locked,attr"`
	DateSent          AndroidTS     `xml:"date_sent,attr"`
	ReadableDate      string        `xml:"readable_date,attr"`
	ContactName       string        `xml:"contact_name,attr"`
	Seen              BoolValue     `xml:"seen,attr"`
	FromAddress       PhoneNumber   `xml:"from_address,attr"`
	Address           PhoneNumber   `xml:"address,attr"`
	MessageClassifier string        `xml:"m_cls,attr"`
	MessageSize       string        `xml:"m_size,attr"`
	Parts             []MMSPart     `xml:"parts>part"`
	Addresses         []PhoneNumber `xml:"addrs>addr"`
	Body              string        `xml:"body"`
}

type MMSPart struct {
	XMLName        xml.Name `xml:"part"`
	Data           string   `xml:"data,attr"`
	ContentDisplay string   `xml:"cd,attr"`
	ContentType    string   `xml:"ct,attr"`
	Filename       string   `xml:"cl,attr"`
	Name           string   `xml:"name,attr"`
	Text           string   `xml:"text,attr"`
	Type           string   `xml:"ct"`
}

// Call represents a call log entry
type Call struct {
	XMLName xml.Name `xml:"call"`
	Number  string   `xml:"number"`
	Date    string   `xml:"date"`
	Type    int      `xml:"type"`
}
