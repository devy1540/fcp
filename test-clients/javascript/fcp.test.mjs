import assert from 'node:assert/strict'
import test from 'node:test'

import {
  AbortMultipartUploadCommand,
  CopyObjectCommand,
  CreateBucketCommand,
  CreateMultipartUploadCommand,
  GetObjectCommand,
  HeadObjectCommand,
  ListMultipartUploadsCommand,
  ListPartsCommand,
  ListObjectsV2Command,
  PutObjectCommand,
  S3Client,
  UploadPartCommand,
} from '@aws-sdk/client-s3'
import {
  ChangeMessageVisibilityCommand,
  CreateQueueCommand,
  DeleteMessageCommand,
  GetQueueAttributesCommand,
  PurgeQueueCommand,
  ReceiveMessageCommand,
  SetQueueAttributesCommand,
  SendMessageBatchCommand,
  SendMessageCommand,
  SQSClient,
} from '@aws-sdk/client-sqs'
import { getSignedUrl } from '@aws-sdk/s3-request-presigner'
import { Upload } from '@aws-sdk/lib-storage'
import { PubSub } from '@google-cloud/pubsub'
import { Storage } from '@google-cloud/storage'

const projectId = 'podo-local'

test('AWS SDK v3 S3 works with FCP including Range, CopyObject and presigned GET', async (t) => {
  const endpoint = process.env.AWS_ENDPOINT_URL
  assert.ok(endpoint)
  const client = new S3Client({
    endpoint,
    forcePathStyle: true,
    region: 'us-east-1',
    credentials: { accessKeyId: 'test', secretAccessKey: 'test' },
  })
  t.after(() => client.destroy())
  const suffix = `${process.pid}-${Date.now()}`
  const bucket = `aws-sdk-assets-${suffix}`
  await client.send(new CreateBucketCommand({ Bucket: bucket }))
  await client.send(
    new PutObjectCommand({ Bucket: bucket, Key: 'docs/hello.txt', Body: 'hello fcp', ContentType: 'text/plain', Metadata: { source: 'aws-sdk-v3' } }),
  )

  const head = await client.send(new HeadObjectCommand({ Bucket: bucket, Key: 'docs/hello.txt' }))
  assert.equal(head.ContentLength, 9)
  assert.equal(head.ContentType, 'text/plain')
  assert.equal(head.Metadata.source, 'aws-sdk-v3')

  const ranged = await client.send(new GetObjectCommand({ Bucket: bucket, Key: 'docs/hello.txt', Range: 'bytes=1-3' }))
  assert.equal(ranged.ContentRange, 'bytes 1-3/9')
  assert.equal(await ranged.Body.transformToString(), 'ell')

  await client.send(new CopyObjectCommand({ Bucket: bucket, Key: 'docs/copied.txt', CopySource: `${bucket}/docs/hello.txt` }))
  const copied = await client.send(new GetObjectCommand({ Bucket: bucket, Key: 'docs/copied.txt' }))
  assert.equal(await copied.Body.transformToString(), 'hello fcp')
  assert.equal(copied.Metadata.source, 'aws-sdk-v3')

  const listed = await client.send(new ListObjectsV2Command({ Bucket: bucket, Prefix: 'docs/' }))
  assert.deepEqual(
    listed.Contents.map((object) => object.Key).sort(),
    ['docs/copied.txt', 'docs/hello.txt'],
  )

  const signedUrl = await getSignedUrl(client, new GetObjectCommand({ Bucket: bucket, Key: 'docs/copied.txt' }), { expiresIn: 60 })
  const signedResponse = await fetch(signedUrl)
  assert.equal(signedResponse.status, 200)
  assert.equal(await signedResponse.text(), 'hello fcp')
})

test('AWS SDK v3 S3 multipart upload persists parts, completes and aborts', async (t) => {
  const endpoint = process.env.AWS_ENDPOINT_URL
  assert.ok(endpoint)
  const client = new S3Client({
    endpoint,
    forcePathStyle: true,
    region: 'us-east-1',
    credentials: { accessKeyId: 'test', secretAccessKey: 'test' },
  })
  t.after(() => client.destroy())
  const bucket = `aws-sdk-multipart-${process.pid}-${Date.now()}`
  await client.send(new CreateBucketCommand({ Bucket: bucket }))

  const partSize = 5 * 1024 * 1024
  const body = Buffer.concat([Buffer.alloc(partSize, 'a'), Buffer.alloc(partSize, 'b'), Buffer.from('tail')])
  const upload = new Upload({
    client,
    queueSize: 2,
    partSize,
    params: {
      Bucket: bucket,
      Key: 'large/archive.bin',
      Body: body,
      ContentType: 'application/octet-stream',
      Metadata: { source: 'aws-sdk-lib-storage' },
    },
  })
  const completed = await upload.done()
  assert.match(completed.ETag, /^"[a-f0-9]{32}-3"$/)

  const downloaded = await client.send(new GetObjectCommand({ Bucket: bucket, Key: 'large/archive.bin' }))
  assert.equal(downloaded.ContentLength, body.length)
  assert.equal(downloaded.Metadata.source, 'aws-sdk-lib-storage')
  assert.deepEqual(Buffer.from(await downloaded.Body.transformToByteArray()), body)

  const initiated = await client.send(
    new CreateMultipartUploadCommand({ Bucket: bucket, Key: 'large/aborted.bin', ContentType: 'application/octet-stream' }),
  )
  assert.ok(initiated.UploadId)
  const activeUploads = await client.send(new ListMultipartUploadsCommand({ Bucket: bucket, Prefix: 'large/' }))
  assert.ok(activeUploads.Uploads.some((candidate) => candidate.UploadId === initiated.UploadId && candidate.Key === 'large/aborted.bin'))
  await client.send(
    new UploadPartCommand({ Bucket: bucket, Key: 'large/aborted.bin', UploadId: initiated.UploadId, PartNumber: 1, Body: 'discarded' }),
  )
  const beforeAbort = await client.send(
    new ListPartsCommand({ Bucket: bucket, Key: 'large/aborted.bin', UploadId: initiated.UploadId }),
  )
  assert.equal(beforeAbort.Parts.length, 1)
  assert.equal(beforeAbort.Parts[0].Size, 9)
  await client.send(
    new AbortMultipartUploadCommand({ Bucket: bucket, Key: 'large/aborted.bin', UploadId: initiated.UploadId }),
  )
  await assert.rejects(
    client.send(new ListPartsCommand({ Bucket: bucket, Key: 'large/aborted.bin', UploadId: initiated.UploadId })),
    (error) => error.name === 'NoSuchUpload',
  )
})

test('AWS SDK v3 SQS works with FCP visibility, batch and purge flows', async (t) => {
  const endpoint = process.env.AWS_ENDPOINT_URL
  assert.ok(endpoint)
  const client = new SQSClient({
    endpoint,
    region: 'us-east-1',
    credentials: { accessKeyId: 'test', secretAccessKey: 'test' },
  })
  t.after(() => client.destroy())
  const queueName = `aws-sdk-jobs-${process.pid}-${Date.now()}`
  const created = await client.send(new CreateQueueCommand({ QueueName: queueName, Attributes: { VisibilityTimeout: '30' } }))
  assert.ok(created.QueueUrl)

  const sent = await client.send(
    new SendMessageCommand({
      QueueUrl: created.QueueUrl,
      MessageBody: 'single-job',
      MessageAttributes: { source: { DataType: 'String', StringValue: 'aws-sdk-v3' } },
    }),
  )
  assert.match(sent.MD5OfMessageAttributes, /^[a-f0-9]{32}$/)
  const firstReceive = await client.send(
    new ReceiveMessageCommand({ QueueUrl: created.QueueUrl, MaxNumberOfMessages: 1, MessageAttributeNames: ['All'] }),
  )
  assert.equal(firstReceive.Messages.length, 1)
  assert.equal(firstReceive.Messages[0].Body, 'single-job')
  assert.equal(firstReceive.Messages[0].MessageAttributes.source.StringValue, 'aws-sdk-v3')
  assert.equal(firstReceive.Messages[0].MD5OfMessageAttributes, sent.MD5OfMessageAttributes)

  await client.send(
    new ChangeMessageVisibilityCommand({ QueueUrl: created.QueueUrl, ReceiptHandle: firstReceive.Messages[0].ReceiptHandle, VisibilityTimeout: 0 }),
  )
  const secondReceive = await client.send(new ReceiveMessageCommand({ QueueUrl: created.QueueUrl, MaxNumberOfMessages: 1 }))
  assert.equal(secondReceive.Messages[0].MessageId, firstReceive.Messages[0].MessageId)
  await client.send(new DeleteMessageCommand({ QueueUrl: created.QueueUrl, ReceiptHandle: secondReceive.Messages[0].ReceiptHandle }))

  const batch = await client.send(
    new SendMessageBatchCommand({
      QueueUrl: created.QueueUrl,
      Entries: [
        { Id: 'a', MessageBody: 'batch-a' },
        { Id: 'b', MessageBody: 'batch-b' },
      ],
    }),
  )
  assert.equal(batch.Successful.length, 2)
  await client.send(new PurgeQueueCommand({ QueueUrl: created.QueueUrl }))
  const attributes = await client.send(new GetQueueAttributesCommand({ QueueUrl: created.QueueUrl, AttributeNames: ['All'] }))
  assert.equal(attributes.Attributes.ApproximateNumberOfMessages, '0')
})

test('AWS SDK v3 SQS moves messages to a configured dead-letter queue', async (t) => {
  const endpoint = process.env.AWS_ENDPOINT_URL
  assert.ok(endpoint)
  const client = new SQSClient({
    endpoint,
    region: 'us-east-1',
    credentials: { accessKeyId: 'test', secretAccessKey: 'test' },
  })
  t.after(() => client.destroy())
  const suffix = `${process.pid}-${Date.now()}`
  const dlq = await client.send(new CreateQueueCommand({ QueueName: `aws-sdk-dlq-${suffix}` }))
  const dlqAttributes = await client.send(
    new GetQueueAttributesCommand({ QueueUrl: dlq.QueueUrl, AttributeNames: ['QueueArn'] }),
  )
  const source = await client.send(
    new CreateQueueCommand({ QueueName: `aws-sdk-redrive-${suffix}`, Attributes: { VisibilityTimeout: '0' } }),
  )
  await assert.rejects(
    client.send(
      new SetQueueAttributesCommand({
        QueueUrl: source.QueueUrl,
        Attributes: {
          RedrivePolicy: JSON.stringify({
            deadLetterTargetArn: `arn:aws:sqs:us-east-1:000000000000:missing-${suffix}`,
            maxReceiveCount: '2',
          }),
        },
      }),
    ),
    (error) => error.name === 'InvalidAttributeValue',
  )
  const redrivePolicy = JSON.stringify({ deadLetterTargetArn: dlqAttributes.Attributes.QueueArn, maxReceiveCount: '2' })
  await client.send(
    new SetQueueAttributesCommand({ QueueUrl: source.QueueUrl, Attributes: { RedrivePolicy: redrivePolicy } }),
  )
  const configured = await client.send(
    new GetQueueAttributesCommand({ QueueUrl: source.QueueUrl, AttributeNames: ['RedrivePolicy'] }),
  )
  assert.deepEqual(JSON.parse(configured.Attributes.RedrivePolicy), JSON.parse(redrivePolicy))

  await client.send(new SendMessageCommand({ QueueUrl: source.QueueUrl, MessageBody: 'poison-job' }))
  const first = await client.send(
    new ReceiveMessageCommand({ QueueUrl: source.QueueUrl, MaxNumberOfMessages: 1, AttributeNames: ['ApproximateReceiveCount'] }),
  )
  const second = await client.send(
    new ReceiveMessageCommand({ QueueUrl: source.QueueUrl, MaxNumberOfMessages: 1, AttributeNames: ['ApproximateReceiveCount'] }),
  )
  assert.equal(first.Messages[0].Attributes.ApproximateReceiveCount, '1')
  assert.equal(second.Messages[0].Attributes.ApproximateReceiveCount, '2')

  const afterLimit = await client.send(new ReceiveMessageCommand({ QueueUrl: source.QueueUrl, MaxNumberOfMessages: 1 }))
  assert.equal((afterLimit.Messages ?? []).length, 0)
  const deadLetter = await client.send(new ReceiveMessageCommand({ QueueUrl: dlq.QueueUrl, MaxNumberOfMessages: 1 }))
  assert.equal(deadLetter.Messages.length, 1)
  assert.equal(deadLetter.Messages[0].Body, 'poison-job')
})

test('AWS SDK v3 SQS FIFO preserves group order and deduplicates sends', async (t) => {
  const endpoint = process.env.AWS_ENDPOINT_URL
  assert.ok(endpoint)
  const client = new SQSClient({
    endpoint,
    region: 'us-east-1',
    credentials: { accessKeyId: 'test', secretAccessKey: 'test' },
  })
  t.after(() => client.destroy())
  const suffix = `${process.pid}-${Date.now()}`

  await assert.rejects(
    client.send(new CreateQueueCommand({ QueueName: `invalid-fifo-${suffix}`, Attributes: { FifoQueue: 'true' } })),
    (error) => error.name === 'InvalidAttributeValue',
  )

  const created = await client.send(
    new CreateQueueCommand({
      QueueName: `aws-sdk-orders-${suffix}.fifo`,
      Attributes: { FifoQueue: 'true', VisibilityTimeout: '30' },
    }),
  )
  await assert.rejects(
    client.send(
      new SendMessageCommand({ QueueUrl: created.QueueUrl, MessageBody: 'missing-group', MessageDeduplicationId: 'missing-group' }),
    ),
    (error) => error.name === 'MissingParameter',
  )
  await assert.rejects(
    client.send(
      new SendMessageCommand({
        QueueUrl: created.QueueUrl,
        MessageBody: 'delayed-fifo',
        MessageGroupId: 'group-a',
        MessageDeduplicationId: 'delayed-fifo',
        DelaySeconds: 1,
      }),
    ),
    (error) => error.name === 'InvalidParameterValue',
  )

  const firstA = await client.send(
    new SendMessageCommand({
      QueueUrl: created.QueueUrl,
      MessageBody: 'group-a-1',
      MessageGroupId: 'group-a',
      MessageDeduplicationId: 'group-a-1',
    }),
  )
  const duplicateA = await client.send(
    new SendMessageCommand({
      QueueUrl: created.QueueUrl,
      MessageBody: 'group-a-1-duplicate-body',
      MessageGroupId: 'group-a',
      MessageDeduplicationId: 'group-a-1',
    }),
  )
  assert.ok(firstA.SequenceNumber)
  assert.equal(duplicateA.MessageId, firstA.MessageId)
  assert.equal(duplicateA.SequenceNumber, firstA.SequenceNumber)

  const secondA = await client.send(
    new SendMessageCommand({
      QueueUrl: created.QueueUrl,
      MessageBody: 'group-a-2',
      MessageGroupId: 'group-a',
      MessageDeduplicationId: 'group-a-2',
    }),
  )
  await client.send(
    new SendMessageCommand({
      QueueUrl: created.QueueUrl,
      MessageBody: 'group-b-1',
      MessageGroupId: 'group-b',
      MessageDeduplicationId: 'group-b-1',
    }),
  )
  assert.ok(BigInt(secondA.SequenceNumber) > BigInt(firstA.SequenceNumber))

  const queued = await client.send(
    new GetQueueAttributesCommand({ QueueUrl: created.QueueUrl, AttributeNames: ['All'] }),
  )
  assert.equal(queued.Attributes.FifoQueue, 'true')
  assert.equal(queued.Attributes.ApproximateNumberOfMessages, '3')

  const receiveOne = (queueUrl) =>
    client.send(new ReceiveMessageCommand({ QueueUrl: queueUrl, MaxNumberOfMessages: 1, AttributeNames: ['All'] }))
  const firstReceive = await receiveOne(created.QueueUrl)
  assert.equal(firstReceive.Messages[0].Body, 'group-a-1')
  assert.equal(firstReceive.Messages[0].Attributes.MessageGroupId, 'group-a')
  assert.equal(firstReceive.Messages[0].Attributes.MessageDeduplicationId, 'group-a-1')
  assert.equal(firstReceive.Messages[0].Attributes.SequenceNumber, firstA.SequenceNumber)

  const otherGroup = await receiveOne(created.QueueUrl)
  assert.equal(otherGroup.Messages[0].Body, 'group-b-1')
  await client.send(
    new DeleteMessageCommand({ QueueUrl: created.QueueUrl, ReceiptHandle: otherGroup.Messages[0].ReceiptHandle }),
  )
  await client.send(
    new ChangeMessageVisibilityCommand({ QueueUrl: created.QueueUrl, ReceiptHandle: firstReceive.Messages[0].ReceiptHandle, VisibilityTimeout: 0 }),
  )
  const firstRedelivery = await receiveOne(created.QueueUrl)
  assert.equal(firstRedelivery.Messages[0].MessageId, firstReceive.Messages[0].MessageId)
  await client.send(
    new DeleteMessageCommand({ QueueUrl: created.QueueUrl, ReceiptHandle: firstRedelivery.Messages[0].ReceiptHandle }),
  )
  const secondReceive = await receiveOne(created.QueueUrl)
  assert.equal(secondReceive.Messages[0].Body, 'group-a-2')

  const contentBased = await client.send(
    new CreateQueueCommand({
      QueueName: `aws-sdk-content-${suffix}.fifo`,
      Attributes: { FifoQueue: 'true', ContentBasedDeduplication: 'true' },
    }),
  )
  await client.send(
    new SendMessageCommand({
      QueueUrl: contentBased.QueueUrl,
      MessageBody: 'same-body',
      MessageGroupId: 'content-group',
      MessageAttributes: { attempt: { DataType: 'Number', StringValue: '1' } },
    }),
  )
  await client.send(
    new SendMessageCommand({
      QueueUrl: contentBased.QueueUrl,
      MessageBody: 'same-body',
      MessageGroupId: 'content-group',
      MessageAttributes: { attempt: { DataType: 'Number', StringValue: '2' } },
    }),
  )
  const fifoBatch = await client.send(
    new SendMessageBatchCommand({
      QueueUrl: contentBased.QueueUrl,
      Entries: [
        { Id: 'first', MessageBody: 'batch-1', MessageGroupId: 'batch-group' },
        { Id: 'second', MessageBody: 'batch-2', MessageGroupId: 'batch-group' },
      ],
    }),
  )
  assert.equal(fifoBatch.Failed.length, 0)
  assert.equal(fifoBatch.Successful.length, 2)
  assert.ok(fifoBatch.Successful.every((entry) => entry.SequenceNumber))
  const contentAttributes = await client.send(
    new GetQueueAttributesCommand({ QueueUrl: contentBased.QueueUrl, AttributeNames: ['All'] }),
  )
  assert.equal(contentAttributes.Attributes.ContentBasedDeduplication, 'true')
  assert.equal(contentAttributes.Attributes.ApproximateNumberOfMessages, '3')
})

test('PODO JavaScript Storage and Pub/Sub SDK versions work with FCP', async (t) => {
  assert.ok(process.env.STORAGE_EMULATOR_HOST)
  assert.ok(process.env.PUBSUB_EMULATOR_HOST)

  const storage = new Storage({ projectId, apiEndpoint: process.env.STORAGE_EMULATOR_HOST })
  const suffix = `${process.pid}-${Date.now()}`
  const bucket = storage.bucket(`javascript-sdk-assets-${suffix}`)
  await bucket.create()
  await bucket.file('intl/ko.json').save('{"hello":"안녕"}', { resumable: false, contentType: 'application/json' })
  const [body] = await bucket.file('intl/ko.json').download()
  assert.equal(body.toString(), '{"hello":"안녕"}')

  assert.ok(process.env.GOOGLE_APPLICATION_CREDENTIALS)
  const [signedUrl] = await bucket.file('intl/ko.json').getSignedUrl({
    version: 'v4',
    action: 'read',
    expires: Date.now() + 60_000,
  })
  assert.ok(signedUrl.startsWith(`${process.env.STORAGE_EMULATOR_HOST}/`))
  const signedResponse = await fetch(signedUrl)
  assert.equal(signedResponse.status, 200)
  assert.equal(await signedResponse.text(), '{"hello":"안녕"}')

  const pubsub = new PubSub({ projectId })
  t.after(async () => pubsub.close())
  const [topic] = await pubsub.createTopic(`javascript-events-${suffix}`)
  const [subscription] = await topic.createSubscription(`javascript-worker-${suffix}`)
  const received = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Pub/Sub receive timeout')), 5000)
    subscription.once('message', (message) => {
      clearTimeout(timer)
      message.ack()
      resolve(message)
    })
  })
  const messageId = await topic.publishMessage({ data: Buffer.from('javascript sdk'), attributes: { source: 'podo-app' } })
  const message = await received
  assert.equal(message.id, messageId)
  assert.equal(message.data.toString(), 'javascript sdk')
  assert.equal(message.attributes.source, 'podo-app')
})

test('PODO REST calls use FCP Metadata, Secret Manager and KMS', async () => {
  const endpoint = process.env.FCP_HTTP_ENDPOINT
  assert.ok(endpoint)

  const metadataHeaders = { 'Metadata-Flavor': 'Google' }
  const [projectResponse, emailResponse, tokenResponse] = await Promise.all([
    fetch(`${endpoint}/computeMetadata/v1/project/project-id`, { headers: metadataHeaders }),
    fetch(`${endpoint}/computeMetadata/v1/instance/service-accounts/default/email`, { headers: metadataHeaders }),
    fetch(`${endpoint}/computeMetadata/v1/instance/service-accounts/default/token`, { headers: metadataHeaders }),
  ])
  assert.equal(projectResponse.status, 200)
  assert.equal((await projectResponse.text()).trim(), projectId)
  assert.equal(emailResponse.status, 200)
  assert.match((await emailResponse.text()).trim(), /@podo-local\.iam\.gserviceaccount\.com$/)
  const metadataToken = await tokenResponse.json()
  assert.equal(tokenResponse.status, 200)
  assert.equal(metadataToken.token_type, 'Bearer')
  assert.equal(metadataToken.access_token.split('.').length, 3)

  const identityResponse = await fetch(
    `${endpoint}/computeMetadata/v1/instance/service-accounts/default/identity?audience=podo-backend-system-token&format=full`,
    { headers: metadataHeaders },
  )
  assert.equal(identityResponse.status, 200)
  const identityToken = (await identityResponse.text()).trim()
  assert.equal(identityToken.split('.').length, 3)
  const jwksResponse = await fetch(`${endpoint}/oauth2/v3/certs`)
  assert.equal(jwksResponse.status, 200)
  const jwks = await jwksResponse.json()
  assert.equal(jwks.keys.length, 1)

  const secretResponse = await fetch(`${endpoint}/v1/projects/${projectId}/secrets/podo-common/versions/latest:access`, {
    headers: { Authorization: `Bearer ${metadataToken.access_token}` },
  })
  assert.equal(secretResponse.status, 200)
  const secret = await secretResponse.json()
  assert.equal(Buffer.from(secret.payload.data, 'base64').toString(), '{"PODO_NOTIFICATOR_SLACK_TOKEN":""}')

  const key = `projects/${projectId}/locations/asia-northeast3/keyRings/podo-local/cryptoKeys/pii-kek-nonprod`
  const plaintext = Buffer.from('podo pii rest')
  const encryptResponse = await fetch(`${endpoint}/v1/${key}:encrypt`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${metadataToken.access_token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ plaintext: plaintext.toString('base64') }),
  })
  assert.equal(encryptResponse.status, 200)
  const encrypted = await encryptResponse.json()
  const decryptResponse = await fetch(`${endpoint}/v1/${key}:decrypt`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${metadataToken.access_token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ ciphertext: encrypted.ciphertext }),
  })
  assert.equal(decryptResponse.status, 200)
  const decrypted = await decryptResponse.json()
  assert.equal(Buffer.from(decrypted.plaintext, 'base64').toString(), plaintext.toString())
})
