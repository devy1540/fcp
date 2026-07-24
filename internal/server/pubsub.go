package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/devy1540/fcp/internal/state"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type pubSubServer struct {
	pubsubpb.UnimplementedPublisherServer
	pubsubpb.UnimplementedSubscriberServer
	store *state.Store
}

func NewPubSubGRPCServer(store *state.Store) *grpc.Server {
	return NewGCPGRPCServer(store)
}

// NewGCPGRPCServer serves the native gRPC APIs used by Google Cloud client
// libraries on one local endpoint. Each SDK selects the service by its fully
// qualified gRPC service name, so Pub/Sub, Firestore and Secret Manager can
// safely share a listener.
func NewGCPGRPCServer(store *state.Store) *grpc.Server {
	grpcServer := grpc.NewServer()
	service := &pubSubServer{store: store}
	pubsubpb.RegisterPublisherServer(grpcServer, service)
	pubsubpb.RegisterSubscriberServer(grpcServer, service)
	firestorepb.RegisterFirestoreServer(grpcServer, newFirestoreServer(store))
	secretmanagerpb.RegisterSecretManagerServiceServer(grpcServer, newSecretManagerServer(store))
	kmspb.RegisterKeyManagementServiceServer(grpcServer, newKMSServer(store))
	credentialspb.RegisterIAMCredentialsServer(grpcServer, newIAMCredentialsServer(store))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	return grpcServer
}

func (s *pubSubServer) CreateTopic(_ context.Context, topic *pubsubpb.Topic) (*pubsubpb.Topic, error) {
	if topic.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "topic name is required")
	}
	if _, err := s.store.PubSubTopic(topic.GetName()); err == nil {
		return nil, status.Error(codes.AlreadyExists, "topic already exists")
	}
	created, err := s.store.CreatePubSubTopic(topic.GetName(), topic.GetLabels())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return topicProto(created), nil
}

func (s *pubSubServer) GetTopic(_ context.Context, request *pubsubpb.GetTopicRequest) (*pubsubpb.Topic, error) {
	topic, err := s.store.PubSubTopic(request.GetTopic())
	if errors.Is(err, state.ErrPubSubTopicNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return topicProto(topic), nil
}

func (s *pubSubServer) ListTopics(_ context.Context, request *pubsubpb.ListTopicsRequest) (*pubsubpb.ListTopicsResponse, error) {
	all := s.store.ListPubSubTopics(strings.TrimSuffix(request.GetProject(), "/") + "/topics/")
	start := state.DecodePageToken(request.GetPageToken())
	pageSize := int(request.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	result := &pubsubpb.ListTopicsResponse{}
	for _, topic := range all {
		if topic.Name <= start {
			continue
		}
		if len(result.Topics) >= pageSize {
			result.NextPageToken = state.EncodePageToken(result.Topics[len(result.Topics)-1].Name)
			break
		}
		result.Topics = append(result.Topics, topicProto(topic))
	}
	return result, nil
}

func (s *pubSubServer) DeleteTopic(_ context.Context, request *pubsubpb.DeleteTopicRequest) (*emptypb.Empty, error) {
	err := s.store.DeletePubSubTopic(request.GetTopic())
	if errors.Is(err, state.ErrPubSubTopicNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *pubSubServer) Publish(_ context.Context, request *pubsubpb.PublishRequest) (*pubsubpb.PublishResponse, error) {
	messages := make([]state.PubSubMessage, 0, len(request.GetMessages()))
	for _, message := range request.GetMessages() {
		messages = append(messages, state.PubSubMessage{Data: append([]byte(nil), message.GetData()...), Attributes: message.GetAttributes(), OrderingKey: message.GetOrderingKey()})
	}
	ids, err := s.store.PublishPubSub(request.GetTopic(), messages)
	if errors.Is(err, state.ErrPubSubTopicNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pubsubpb.PublishResponse{MessageIds: ids}, nil
}

func (s *pubSubServer) ListTopicSubscriptions(_ context.Context, request *pubsubpb.ListTopicSubscriptionsRequest) (*pubsubpb.ListTopicSubscriptionsResponse, error) {
	if _, err := s.store.PubSubTopic(request.GetTopic()); errors.Is(err, state.ErrPubSubTopicNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	all := s.store.PubSubTopicSubscriptions(request.GetTopic())
	start := state.DecodePageToken(request.GetPageToken())
	pageSize := int(request.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	result := &pubsubpb.ListTopicSubscriptionsResponse{}
	for _, name := range all {
		if name <= start {
			continue
		}
		if len(result.Subscriptions) >= pageSize {
			result.NextPageToken = state.EncodePageToken(result.Subscriptions[len(result.Subscriptions)-1])
			break
		}
		result.Subscriptions = append(result.Subscriptions, name)
	}
	return result, nil
}

func (s *pubSubServer) CreateSubscription(_ context.Context, request *pubsubpb.Subscription) (*pubsubpb.Subscription, error) {
	if request.GetName() == "" || request.GetTopic() == "" {
		return nil, status.Error(codes.InvalidArgument, "subscription name and topic are required")
	}
	if _, err := s.store.PubSubSubscription(request.GetName()); err == nil {
		return nil, status.Error(codes.AlreadyExists, "subscription already exists")
	}
	created, err := s.store.CreatePubSubSubscription(request.GetName(), request.GetTopic(), request.GetAckDeadlineSeconds(), request.GetLabels(), request.GetEnableMessageOrdering())
	if errors.Is(err, state.ErrPubSubTopicNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if policy := request.GetDeadLetterPolicy(); policy != nil {
		created, err = s.store.UpdatePubSubSubscription(created.Name, 0, nil, policy.GetDeadLetterTopic(), policy.GetMaxDeliveryAttempts(), false, false, true)
		if err != nil {
			return nil, pubSubStateError(err)
		}
	}
	return subscriptionProto(created), nil
}

func (s *pubSubServer) UpdateSubscription(_ context.Context, request *pubsubpb.UpdateSubscriptionRequest) (*pubsubpb.Subscription, error) {
	requested := request.GetSubscription()
	if requested.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "subscription name is required")
	}
	mask := request.GetUpdateMask()
	if mask == nil || len(mask.GetPaths()) == 0 {
		mask = &fieldmaskpb.FieldMask{Paths: []string{"ack_deadline_seconds", "labels", "dead_letter_policy"}}
	}
	var updateDeadline, updateLabels, updateDeadLetter bool
	for _, path := range mask.GetPaths() {
		switch path {
		case "ack_deadline_seconds":
			updateDeadline = true
		case "labels":
			updateLabels = true
		case "dead_letter_policy":
			updateDeadLetter = true
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unsupported update path %q", path)
		}
	}
	policy := requested.GetDeadLetterPolicy()
	deadLetterTopic := ""
	maxAttempts := int32(0)
	if policy != nil {
		deadLetterTopic = policy.GetDeadLetterTopic()
		maxAttempts = policy.GetMaxDeliveryAttempts()
		if deadLetterTopic == "" || maxAttempts < 5 || maxAttempts > 100 {
			return nil, status.Error(codes.InvalidArgument, "dead letter policy requires a topic and max_delivery_attempts between 5 and 100")
		}
	}
	updated, err := s.store.UpdatePubSubSubscription(requested.GetName(), requested.GetAckDeadlineSeconds(), requested.GetLabels(), deadLetterTopic, maxAttempts, updateDeadline, updateLabels, updateDeadLetter)
	if err != nil {
		return nil, pubSubStateError(err)
	}
	return subscriptionProto(updated), nil
}

func pubSubStateError(err error) error {
	switch {
	case errors.Is(err, state.ErrPubSubSubscriptionNotFound), errors.Is(err, state.ErrPubSubTopicNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func (s *pubSubServer) GetSubscription(_ context.Context, request *pubsubpb.GetSubscriptionRequest) (*pubsubpb.Subscription, error) {
	sub, err := s.store.PubSubSubscription(request.GetSubscription())
	if errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return subscriptionProto(sub), nil
}

func (s *pubSubServer) ListSubscriptions(_ context.Context, request *pubsubpb.ListSubscriptionsRequest) (*pubsubpb.ListSubscriptionsResponse, error) {
	all := s.store.ListPubSubSubscriptions(strings.TrimSuffix(request.GetProject(), "/") + "/subscriptions/")
	start := state.DecodePageToken(request.GetPageToken())
	pageSize := int(request.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	result := &pubsubpb.ListSubscriptionsResponse{}
	for _, sub := range all {
		if sub.Name <= start {
			continue
		}
		if len(result.Subscriptions) >= pageSize {
			result.NextPageToken = state.EncodePageToken(result.Subscriptions[len(result.Subscriptions)-1].Name)
			break
		}
		result.Subscriptions = append(result.Subscriptions, subscriptionProto(sub))
	}
	return result, nil
}

func (s *pubSubServer) DeleteSubscription(_ context.Context, request *pubsubpb.DeleteSubscriptionRequest) (*emptypb.Empty, error) {
	err := s.store.DeletePubSubSubscription(request.GetSubscription())
	if errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *pubSubServer) Pull(_ context.Context, request *pubsubpb.PullRequest) (*pubsubpb.PullResponse, error) {
	if request.GetMaxMessages() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "max_messages must be positive")
	}
	sub, err := s.store.PubSubSubscription(request.GetSubscription())
	if errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	messages, err := s.store.PullPubSub(request.GetSubscription(), int(request.GetMaxMessages()), sub.AckDeadlineSeconds)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pubsubpb.PullResponse{ReceivedMessages: receivedProtos(messages, sub.DeadLetterTopic != "")}, nil
}

func (s *pubSubServer) Acknowledge(_ context.Context, request *pubsubpb.AcknowledgeRequest) (*emptypb.Empty, error) {
	err := s.store.AckPubSub(request.GetSubscription(), request.GetAckIds())
	if errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *pubSubServer) ModifyAckDeadline(_ context.Context, request *pubsubpb.ModifyAckDeadlineRequest) (*emptypb.Empty, error) {
	err := s.store.ModifyPubSubAckDeadline(request.GetSubscription(), request.GetAckIds(), []int32{request.GetAckDeadlineSeconds()})
	if errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *pubSubServer) StreamingPull(stream pubsubpb.Subscriber_StreamingPullServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetSubscription() == "" {
		return status.Error(codes.InvalidArgument, "subscription is required in the first request")
	}
	if _, err := s.store.PubSubSubscription(first.GetSubscription()); errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	subscription := first.GetSubscription()
	sub, _ := s.store.PubSubSubscription(subscription)
	hasDeadLetterPolicy := sub.DeadLetterTopic != ""
	deadline := first.GetStreamAckDeadlineSeconds()
	if deadline <= 0 {
		deadline = 10
	}
	requests := make(chan *pubsubpb.StreamingPullRequest, 8)
	receiveErrors := make(chan error, 1)
	go func() {
		for {
			request, err := stream.Recv()
			if err != nil {
				receiveErrors <- err
				return
			}
			requests <- request
		}
	}()
	apply := func(request *pubsubpb.StreamingPullRequest) error {
		if len(request.GetAckIds()) > 0 {
			if err := s.store.AckPubSub(subscription, request.GetAckIds()); err != nil {
				return err
			}
		}
		if len(request.GetModifyDeadlineAckIds()) > 0 {
			if len(request.GetModifyDeadlineAckIds()) != len(request.GetModifyDeadlineSeconds()) {
				return status.Error(codes.InvalidArgument, "modify ack ID and deadline counts differ")
			}
			if err := s.store.ModifyPubSubAckDeadline(subscription, request.GetModifyDeadlineAckIds(), request.GetModifyDeadlineSeconds()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := apply(first); err != nil {
		return err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case err := <-receiveErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case request := <-requests:
			if request.GetStreamAckDeadlineSeconds() > 0 {
				deadline = request.GetStreamAckDeadlineSeconds()
			}
			if err := apply(request); err != nil {
				return err
			}
		case <-ticker.C:
			messages, err := s.store.PullPubSub(subscription, 100, deadline)
			if err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			if len(messages) == 0 {
				continue
			}
			if err := stream.Send(&pubsubpb.StreamingPullResponse{ReceivedMessages: receivedProtos(messages, hasDeadLetterPolicy)}); err != nil {
				return err
			}
		}
	}
}

func topicProto(topic state.PubSubTopic) *pubsubpb.Topic {
	return &pubsubpb.Topic{Name: topic.Name, Labels: topic.Labels}
}

func subscriptionProto(sub state.PubSubSubscription) *pubsubpb.Subscription {
	result := &pubsubpb.Subscription{Name: sub.Name, Topic: sub.Topic, AckDeadlineSeconds: sub.AckDeadlineSeconds, Labels: sub.Labels, EnableMessageOrdering: sub.EnableOrdering, State: pubsubpb.Subscription_ACTIVE}
	if sub.DeadLetterTopic != "" {
		result.DeadLetterPolicy = &pubsubpb.DeadLetterPolicy{DeadLetterTopic: sub.DeadLetterTopic, MaxDeliveryAttempts: sub.MaxDeliveryAttempts}
	}
	return result
}

func receivedProtos(messages []state.PubSubMessage, includeDeliveryAttempt bool) []*pubsubpb.ReceivedMessage {
	result := make([]*pubsubpb.ReceivedMessage, 0, len(messages))
	for _, message := range messages {
		deliveryAttempt := int32(0)
		if includeDeliveryAttempt {
			deliveryAttempt = message.DeliveryAttempt
		}
		result = append(result, &pubsubpb.ReceivedMessage{
			AckId:   message.AckID,
			Message: &pubsubpb.PubsubMessage{Data: message.Data, Attributes: message.Attributes, MessageId: message.MessageID, PublishTime: timestamppb.New(message.PublishTime), OrderingKey: message.OrderingKey},
			// Pub/Sub only exposes delivery_attempt when a dead-letter policy is configured.
			DeliveryAttempt: deliveryAttempt,
		})
	}
	return result
}
