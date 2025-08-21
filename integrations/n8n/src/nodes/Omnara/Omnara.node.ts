import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeType,
	INodeTypeDescription,
	NodeOperationError,
	NodeConnectionType,
	IWebhookFunctions,
	IWebhookResponseData,
	IDataObject,
	SEND_AND_WAIT_OPERATION,
} from 'n8n-workflow';

import * as message from './actions/message';
import * as session from './actions/session';
import { omnaraSendAndWaitWebhook } from '../../utils/sendAndWaitWebhook';
import { sendAndWaitWebhooksDescription } from '../../utils/sendAndWait/descriptions';
import { configureWaitTillDate } from '../../utils/sendAndWait/configureWaitTillDate';
import { omnaraApiRequest } from '../../utils/GenericFunctions';

export class Omnara implements INodeType {
	webhook = omnaraSendAndWaitWebhook;
	
	description: INodeTypeDescription = {
		displayName: 'Omnara',
		name: 'omnara',
		icon: 'file:omnara.svg',
		group: ['transform'],
		version: 1,
		subtitle: '={{$parameter["operation"] + ": " + $parameter["resource"]}}',
		description: 'Interact with Omnara AI agents',
		defaults: {
			name: 'Omnara',
		},
		inputs: [NodeConnectionType.Main],
		outputs: [NodeConnectionType.Main],
		webhooks: sendAndWaitWebhooksDescription,
		credentials: [
			{
				name: 'omnaraApi',
				required: true,
			},
		],
		properties: [
			{
				displayName: 'Resource',
				name: 'resource',
				type: 'options',
				noDataExpression: true,
				options: [
					{
						name: 'Message',
						value: 'message',
						description: 'Send messages to Omnara agents',
					},
					{
						name: 'Session',
						value: 'session',
						description: 'Manage agent sessions',
					},
				],
				default: 'message',
			},
			...message.descriptions,
			...session.descriptions,
		],
		usableAsTool: true,
	};

	async execute(this: IExecuteFunctions): Promise<INodeExecutionData[][]> {
		const items = this.getInputData();
		const returnData: INodeExecutionData[] = [];
		const resource = this.getNodeParameter('resource', 0) as string;
		const operation = this.getNodeParameter('operation', 0) as string;
		
		console.log('Omnara Node - Resource:', resource, 'Operation:', operation);
		console.log('SEND_AND_WAIT_OPERATION constant:', SEND_AND_WAIT_OPERATION);

		for (let i = 0; i < items.length; i++) {
			try {
				let responseData: INodeExecutionData[] = [];

				if (resource === 'message') {
					if (operation === 'send') {
						responseData = await message.send.execute.call(this, i);
					} else if (operation === SEND_AND_WAIT_OPERATION || operation === 'sendAndWait') {
						// FOLLOWING SLACK PATTERN EXACTLY
						const agentInstanceId = this.getNodeParameter('agentInstanceId', 0) as string;
						const agentType = this.getNodeParameter('agentType', 0) as string;
						const messageContent = this.getNodeParameter('message', 0) as string;
						const options = this.getNodeParameter('options', 0, {}) as any;
						
						// Check if sync mode is enabled (for AI Agent compatibility)
						const syncMode = options.syncMode || false;
						
						if (syncMode) {
							console.log('Sync mode enabled - using polling for AI Agent compatibility');
							
							// In sync mode, we poll for responses instead of using putExecutionToWait
							const syncTimeout = options.syncTimeout || 300; // Default 5 minutes
							const pollInterval = options.pollInterval || 2; // Default 2 seconds
							
							// Send the message first
							const body: any = {
								agent_instance_id: agentInstanceId,
								agent_type: agentType,
								content: messageContent,
								requires_user_input: true,
								// Don't send webhook URL in sync mode
								message_metadata: {
									sync_mode: true,
									node_id: this.getNode().id,
								},
							};
							
							if (options.sendEmail !== undefined) {
								body.send_email = options.sendEmail;
							}
							if (options.sendSms !== undefined) {
								body.send_sms = options.sendSms;
							}
							if (options.sendPush !== undefined) {
								body.send_push = options.sendPush;
							}
							
							console.log('Sending message in sync mode...');
							const sendResponse = await omnaraApiRequest.call(
								this,
								'POST',
								'/messages/agent',
								body,
							);
							
							const messageId = sendResponse.message_id;
							console.log('Message sent. Message ID:', messageId, 'Agent Instance:', agentInstanceId);
							
							// Now poll for responses
							const startTime = Date.now();
							const timeoutMs = syncTimeout * 1000;
							let lastReadMessageId = messageId;
							
							while (Date.now() - startTime < timeoutMs) {
								// Wait for poll interval
								await new Promise(resolve => setTimeout(resolve, pollInterval * 1000));
								
								// Check for pending messages
								console.log('Polling for user response...');
								try {
									const pendingResponse = await omnaraApiRequest.call(
										this,
										'GET',
										'/messages/pending',
										{},
										{
											agent_instance_id: agentInstanceId,
											last_read_message_id: lastReadMessageId,
										},
									);
									
									console.log('Poll response:', pendingResponse);
									
									// Check if we got any user messages
									if (pendingResponse.messages && pendingResponse.messages.length > 0) {
										console.log('Got user response(s)!', pendingResponse.messages.length);
										
										// Return the last user message as the result
										const lastMessage = pendingResponse.messages[pendingResponse.messages.length - 1];
										
										// Return the response in a format compatible with the workflow
										return [[{
											json: {
												success: true,
												agent_instance_id: agentInstanceId,
												user_response: lastMessage.content,
												message_id: lastMessage.id,
												response_time: lastMessage.created_at,
												all_responses: pendingResponse.messages.map((m: any) => ({
													content: m.content,
													id: m.id,
													created_at: m.created_at,
												})),
											},
											pairedItem: i,
										}]];
									}
									
									// If status is 'stale', update our last_read_message_id
									if (pendingResponse.status === 'stale') {
										console.log('Status is stale, updating last_read_message_id');
										// We might need to re-fetch to get the current state
										// For now, continue polling
									}
								} catch (pollError) {
									console.error('Error polling for messages:', pollError);
									// Continue polling on error
								}
							}
							
							// Timeout reached without response
							console.log('Sync mode timeout reached without user response');
							return [[{
								json: {
									success: false,
									agent_instance_id: agentInstanceId,
									error: 'Timeout waiting for user response',
									timeout_seconds: syncTimeout,
								},
								pairedItem: i,
							}]];
							
						} else {
							// Original async mode with putExecutionToWait
							// Get webhook information for resume
							const executionId = this.getExecutionId();
							const nodeId = this.getNode().id;
							
							// Get the base URL for webhooks - this should be your n8n instance URL
							// For now, we'll use the environment variable or a config option
							const n8nBaseUrl = process.env.N8N_BASE_URL || 'http://localhost:5678';
							
							// Build the full webhook URL that Omnara should call
							// This follows n8n's webhook-waiting pattern
							const webhookUrl = `${n8nBaseUrl}/webhook-waiting/${executionId}/${nodeId}`;
							
							console.log('Webhook URL for resume:', webhookUrl);
							
							const body: any = {
								agent_instance_id: agentInstanceId,
								agent_type: agentType,
								content: messageContent,
								requires_user_input: true,
								// Send webhook URL in metadata so Omnara knows where to send the response
								message_metadata: {
									webhook_url: webhookUrl,  // Full URL that backend expects
									execution_id: executionId,
									node_id: nodeId,
									webhook_type: 'n8n_send_and_wait',
								},
							};
							
							if (options.sendEmail !== undefined) {
								body.send_email = options.sendEmail;
							}
							if (options.sendSms !== undefined) {
								body.send_sms = options.sendSms;
							}
							if (options.sendPush !== undefined) {
								body.send_push = options.sendPush;
							}
							
							console.log('Sending message to Omnara with metadata:', JSON.stringify(body.message_metadata, null, 2));
							
							// Send message to Omnara (like Slack sends to chat.postMessage)
							const response = await omnaraApiRequest.call(
								this,
								'POST',
								'/messages/agent',
								body,
							);
							
							console.log('Message sent successfully. Message ID:', response.message_id);
							
							// Configure wait time
							const waitTill = configureWaitTillDate(this, 0);
							
							console.log('ABOUT TO WAIT! waitTill:', waitTill);
							console.log('Calling putExecutionToWait...');
							
							// Put execution to wait
							if (waitTill === 'WAIT_INDEFINITELY') {
								console.log('Waiting indefinitely (1 year)');
								await this.putExecutionToWait(new Date(Date.now() + 365 * 24 * 60 * 60 * 1000));
							} else {
								console.log('Waiting until:', waitTill);
								await this.putExecutionToWait(waitTill as Date);
							}
							
							console.log('After putExecutionToWait - this should not print until webhook is called!');
							
							// Return input data
							return [this.getInputData()];
						}
					} else {
						throw new NodeOperationError(
							this.getNode(),
							`The operation "${operation}" is not supported for resource "${resource}"`,
							{ itemIndex: i },
						);
					}
				} else if (resource === 'session') {
					if (operation === 'end') {
						responseData = await session.end.execute.call(this, i);
					} else {
						throw new NodeOperationError(
							this.getNode(),
							`The operation "${operation}" is not supported for resource "${resource}"`,
							{ itemIndex: i },
						);
					}
				} else {
					throw new NodeOperationError(
						this.getNode(),
						`The resource "${resource}" is not supported`,
						{ itemIndex: i },
					);
				}

				returnData.push(...responseData);
			} catch (error) {
				if (this.continueOnFail()) {
					returnData.push({ 
						json: { 
							error: error instanceof Error ? error.message : String(error),
							resource,
							operation,
							itemIndex: i,
						},
						pairedItem: i,
					});
					continue;
				}
				throw error;
			}
		}

		return [returnData];
	}
}