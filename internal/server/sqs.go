package server

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devy1540/fcp/internal/state"
)

type sqsInput struct {
	QueueName              string                            `json:"QueueName"`
	QueueUrl               string                            `json:"QueueUrl"`
	QueueNamePrefix        string                            `json:"QueueNamePrefix"`
	MessageBody            string                            `json:"MessageBody"`
	ReceiptHandle          string                            `json:"ReceiptHandle"`
	MaxNumberOfMessages    int                               `json:"MaxNumberOfMessages"`
	VisibilityTimeout      *int                              `json:"VisibilityTimeout"`
	WaitTimeSeconds        int                               `json:"WaitTimeSeconds"`
	DelaySeconds           *int                              `json:"DelaySeconds"`
	MessageGroupID         string                            `json:"MessageGroupId"`
	MessageDeduplicationID string                            `json:"MessageDeduplicationId"`
	Attributes             map[string]string                 `json:"Attributes"`
	MessageAttributes      map[string]state.MessageAttribute `json:"MessageAttributes"`
	AttributeNames         []string                          `json:"AttributeNames"`
	MessageAttributeNames  []string                          `json:"MessageAttributeNames"`
	Entries                []sqsBatchEntry                   `json:"Entries"`
}

type sqsBatchEntry struct {
	ID                     string                            `json:"Id"`
	MessageBody            string                            `json:"MessageBody"`
	DelaySeconds           *int                              `json:"DelaySeconds"`
	MessageGroupID         string                            `json:"MessageGroupId"`
	MessageDeduplicationID string                            `json:"MessageDeduplicationId"`
	ReceiptHandle          string                            `json:"ReceiptHandle"`
	MessageAttributes      map[string]state.MessageAttribute `json:"MessageAttributes"`
}

func (s *Server) handleSQS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sqsError(w, http.StatusMethodNotAllowed, "UnsupportedOperation", "SQS operations require POST")
		return
	}
	operation := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.")
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		sqsError(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	var input sqsInput
	if len(raw) > 0 && string(raw) != "{}" {
		if err := json.Unmarshal(raw, &input); err != nil {
			sqsError(w, http.StatusBadRequest, "InvalidRequest", err.Error())
			return
		}
	}

	switch operation {
	case "CreateQueue":
		q, err := s.store.CreateQueue(input.QueueName, input.Attributes)
		if errors.Is(err, state.ErrInvalidQueueAttribute) {
			sqsError(w, http.StatusBadRequest, "InvalidAttributeValue", err.Error())
			return
		}
		if err != nil {
			sqsInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"QueueUrl": queueURL(r, q.Name)})
	case "GetQueueUrl":
		if _, err := s.store.Queue(input.QueueName); errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
			return
		} else if err != nil {
			sqsInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"QueueUrl": queueURL(r, input.QueueName)})
	case "ListQueues":
		queues := s.store.ListQueues(input.QueueNamePrefix)
		urls := make([]string, 0, len(queues))
		for _, q := range queues {
			urls = append(urls, queueURL(r, q.Name))
		}
		writeJSON(w, http.StatusOK, map[string]any{"QueueUrls": urls})
	case "DeleteQueue":
		if err := s.store.DeleteQueue(queueName(input.QueueUrl)); errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
		} else if err != nil {
			sqsInternalError(w, err)
		} else {
			writeJSON(w, http.StatusOK, struct{}{})
		}
	case "PurgeQueue":
		if err := s.store.PurgeQueue(queueName(input.QueueUrl)); errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
		} else if err != nil {
			sqsInternalError(w, err)
		} else {
			writeJSON(w, http.StatusOK, struct{}{})
		}
	case "SendMessage":
		delay, delaySpecified := sqsDelay(input.DelaySeconds)
		m, err := s.store.SendMessageWithOptions(
			queueName(input.QueueUrl), input.MessageBody, input.MessageAttributes, delay,
			state.SendMessageOptions{
				MessageGroupID: input.MessageGroupID, MessageDeduplicationID: input.MessageDeduplicationID, DelaySpecified: delaySpecified,
			},
		)
		if errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
			return
		}
		if errors.Is(err, state.ErrMissingMessageParameter) {
			sqsError(w, http.StatusBadRequest, "MissingParameter", err.Error())
			return
		}
		if errors.Is(err, state.ErrInvalidMessageParameter) {
			sqsError(w, http.StatusBadRequest, "InvalidParameterValue", err.Error())
			return
		}
		if err != nil {
			sqsInternalError(w, err)
			return
		}
		response := map[string]string{"MD5OfMessageBody": m.MD5OfBody, "MessageId": m.MessageID}
		if checksum := sqsMessageAttributesMD5(input.MessageAttributes); checksum != "" {
			response["MD5OfMessageAttributes"] = checksum
		}
		if m.SequenceNumber != "" {
			response["SequenceNumber"] = m.SequenceNumber
		}
		writeJSON(w, http.StatusOK, response)
	case "SendMessageBatch":
		s.handleSendBatch(w, input)
	case "ReceiveMessage":
		s.receiveMessages(w, input)
	case "DeleteMessage":
		err := s.store.DeleteMessage(queueName(input.QueueUrl), input.ReceiptHandle)
		if errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
		} else if errors.Is(err, state.ErrReceiptInvalid) {
			sqsError(w, http.StatusBadRequest, "ReceiptHandleIsInvalid", err.Error())
		} else if err != nil {
			sqsInternalError(w, err)
		} else {
			writeJSON(w, http.StatusOK, struct{}{})
		}
	case "DeleteMessageBatch":
		s.handleDeleteBatch(w, input)
	case "ChangeMessageVisibility":
		seconds := 0
		if input.VisibilityTimeout != nil {
			seconds = *input.VisibilityTimeout
		}
		err := s.store.ChangeVisibility(queueName(input.QueueUrl), input.ReceiptHandle, seconds)
		if errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
		} else if errors.Is(err, state.ErrReceiptInvalid) {
			sqsError(w, http.StatusBadRequest, "ReceiptHandleIsInvalid", err.Error())
		} else if err != nil {
			sqsInternalError(w, err)
		} else {
			writeJSON(w, http.StatusOK, struct{}{})
		}
	case "GetQueueAttributes":
		attrs, err := s.store.QueueAttributes(queueName(input.QueueUrl))
		if errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
			return
		}
		if err != nil {
			sqsInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"Attributes": selectAttributes(attrs, input.AttributeNames)})
	case "SetQueueAttributes":
		err := s.store.SetQueueAttributes(queueName(input.QueueUrl), input.Attributes)
		if errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
		} else if errors.Is(err, state.ErrInvalidQueueAttribute) {
			sqsError(w, http.StatusBadRequest, "InvalidAttributeValue", err.Error())
		} else if err != nil {
			sqsInternalError(w, err)
		} else {
			writeJSON(w, http.StatusOK, struct{}{})
		}
	default:
		sqsError(w, http.StatusBadRequest, "UnsupportedOperation", fmt.Sprintf("operation %q is not implemented", operation))
	}
}

func (s *Server) receiveMessages(w http.ResponseWriter, input sqsInput) {
	visibility := -1
	if input.VisibilityTimeout != nil {
		visibility = *input.VisibilityTimeout
	}
	deadline := time.Now().Add(time.Duration(input.WaitTimeSeconds) * time.Second)
	for {
		messages, err := s.store.ReceiveMessages(queueName(input.QueueUrl), input.MaxNumberOfMessages, visibility)
		if errors.Is(err, state.ErrQueueNotFound) {
			sqsQueueNotFound(w)
			return
		}
		if err != nil {
			sqsInternalError(w, err)
			return
		}
		if len(messages) > 0 || !time.Now().Before(deadline) {
			out := make([]map[string]any, 0, len(messages))
			for _, m := range messages {
				attrs := map[string]string{"SenderId": state.AccountID(), "SentTimestamp": strconv.FormatInt(m.SentAt.UnixMilli(), 10), "ApproximateReceiveCount": strconv.Itoa(m.ReceiveCount), "ApproximateFirstReceiveTimestamp": strconv.FormatInt(time.Now().UnixMilli(), 10)}
				if m.MessageGroupID != "" {
					attrs["MessageGroupId"] = m.MessageGroupID
				}
				if m.MessageDeduplicationID != "" {
					attrs["MessageDeduplicationId"] = m.MessageDeduplicationID
				}
				if m.SequenceNumber != "" {
					attrs["SequenceNumber"] = m.SequenceNumber
				}
				item := map[string]any{"MessageId": m.MessageID, "ReceiptHandle": m.ReceiptHandle, "MD5OfBody": m.MD5OfBody, "Body": m.Body, "Attributes": attrs}
				if len(m.MessageAttributes) > 0 {
					item["MessageAttributes"] = m.MessageAttributes
					item["MD5OfMessageAttributes"] = sqsMessageAttributesMD5(m.MessageAttributes)
				}
				out = append(out, item)
			}
			writeJSON(w, http.StatusOK, map[string]any{"Messages": out})
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Server) handleSendBatch(w http.ResponseWriter, input sqsInput) {
	name := queueName(input.QueueUrl)
	successful := []map[string]string{}
	failed := []map[string]any{}
	for _, entry := range input.Entries {
		delay, delaySpecified := sqsDelay(entry.DelaySeconds)
		m, err := s.store.SendMessageWithOptions(
			name, entry.MessageBody, entry.MessageAttributes, delay,
			state.SendMessageOptions{
				MessageGroupID: entry.MessageGroupID, MessageDeduplicationID: entry.MessageDeduplicationID, DelaySpecified: delaySpecified,
			},
		)
		if err != nil {
			code, senderFault := sqsSendError(err)
			failed = append(failed, map[string]any{"Id": entry.ID, "Code": code, "Message": err.Error(), "SenderFault": senderFault})
			continue
		}
		result := map[string]string{"Id": entry.ID, "MessageId": m.MessageID, "MD5OfMessageBody": m.MD5OfBody}
		if checksum := sqsMessageAttributesMD5(entry.MessageAttributes); checksum != "" {
			result["MD5OfMessageAttributes"] = checksum
		}
		if m.SequenceNumber != "" {
			result["SequenceNumber"] = m.SequenceNumber
		}
		successful = append(successful, result)
	}
	writeJSON(w, http.StatusOK, map[string]any{"Successful": successful, "Failed": failed})
}

// SQS hashes message attributes using sorted names, four-byte big-endian
// lengths and a one-byte logical transport type. AWS SDKs verify this value.
func sqsMessageAttributesMD5(attributes map[string]state.MessageAttribute) string {
	if len(attributes) == 0 {
		return ""
	}
	names := make([]string, 0, len(attributes))
	for name := range attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	digest := md5.New()
	for _, name := range names {
		attribute := attributes[name]
		writeSQSMD5Field(digest, []byte(name))
		writeSQSMD5Field(digest, []byte(attribute.DataType))
		if strings.HasPrefix(attribute.DataType, "Binary") {
			_, _ = digest.Write([]byte{2})
			writeSQSMD5Field(digest, attribute.BinaryValue)
		} else {
			_, _ = digest.Write([]byte{1})
			writeSQSMD5Field(digest, []byte(attribute.StringValue))
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeSQSMD5Field(writer io.Writer, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func sqsDelay(value *int) (int, bool) {
	if value == nil {
		return -1, false
	}
	return *value, true
}

func sqsSendError(err error) (string, bool) {
	switch {
	case errors.Is(err, state.ErrQueueNotFound):
		return "AWS.SimpleQueueService.NonExistentQueue", true
	case errors.Is(err, state.ErrMissingMessageParameter):
		return "MissingParameter", true
	case errors.Is(err, state.ErrInvalidMessageParameter):
		return "InvalidParameterValue", true
	default:
		return "InternalError", false
	}
}

func (s *Server) handleDeleteBatch(w http.ResponseWriter, input sqsInput) {
	name := queueName(input.QueueUrl)
	successful := []map[string]string{}
	failed := []map[string]any{}
	for _, entry := range input.Entries {
		if err := s.store.DeleteMessage(name, entry.ReceiptHandle); err != nil {
			failed = append(failed, map[string]any{"Id": entry.ID, "Code": "ReceiptHandleIsInvalid", "Message": err.Error(), "SenderFault": true})
		} else {
			successful = append(successful, map[string]string{"Id": entry.ID})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"Successful": successful, "Failed": failed})
}

func queueURL(r *http.Request, name string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	}
	return scheme + "://" + r.Host + "/" + state.AccountID() + "/" + url.PathEscape(name)
}

func queueName(queueURL string) string {
	parsed, err := url.Parse(queueURL)
	if err == nil {
		queueURL = parsed.Path
	}
	parts := strings.Split(strings.Trim(queueURL, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	name, _ := url.PathUnescape(parts[len(parts)-1])
	return name
}

func selectAttributes(attrs map[string]string, names []string) map[string]string {
	if len(names) == 0 {
		return map[string]string{}
	}
	for _, name := range names {
		if name == "All" {
			return attrs
		}
	}
	result := map[string]string{}
	for _, name := range names {
		if value, ok := attrs[name]; ok {
			result[name] = value
		}
	}
	return result
}

func sqsQueueNotFound(w http.ResponseWriter) {
	sqsError(w, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist")
}
func sqsInternalError(w http.ResponseWriter, err error) {
	sqsError(w, http.StatusInternalServerError, "InternalError", err.Error())
}
func sqsError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("x-amzn-ErrorType", code)
	writeJSON(w, status, map[string]string{"__type": code, "message": message})
}
