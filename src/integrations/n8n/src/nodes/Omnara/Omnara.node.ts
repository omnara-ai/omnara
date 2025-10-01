import {
    IExecuteFunctions,
    INodeExecutionData,
    INodeType,
    INodeTypeDescription,
    NodeConnectionTypes,
    NodeOperationError,
    SEND_AND_WAIT_OPERATION
} from 'n8n-workflow';

import * as message from './actions/message';
import * as session from './actions/session';
import { omnaraSendAndWaitWebhook } from '../../utils/sendAndWaitWebhook';
import { sendAndWaitWebhooksDescription } from '../../utils/sendAndWait/descriptions';
import { configureWaitTillDate } from '../../utils/sendAndWait/configureWaitTillDate';
import { omnaraApiRequest, generateAgentInstanceId, getAgentType } from '../../utils/GenericFunctions';


export class Omnara implements INodeType {
	webhook = omnaraSendAndWaitWebhook;

	description: INodeTypeDescription = {
		displayName: 'Omnara',
		name: 'omnara',
		icon: 'file:omnara.png',
		group: ['transform'],
		version: 1,
		subtitle: '={{$parameter["operation"] + ": " + $parameter["resource"]}}',
		description:
			'Send messages to users via Omnara - a web and mobile platform for human-AI communication',
		defaults: {
			name: 'Omnara',
		},
        inputs: [NodeConnectionTypes.Main],
        outputs: [NodeConnectionTypes.Main],
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
		for (let i = 0; i < items.length; i++) {
			try {
				let responseData: INodeExecutionData[] = [];
				if (resource === 'message') {
					if (operation === 'send') {
						responseData = await message.send.execute.call(this, i);
					} else if (operation === SEND_AND_WAIT_OPERATION || operation === 'sendAndWait') {
						// FOLLOWING SLACK PATTERN EXACTLY		
						const agentType = getAgentType.call(this, 0);				
						const agentInstanceId = generateAgentInstanceId.call(this, 0, agentType);
						const messageContent = this.getNodeParameter('message', 0) as string;
						const options = this.getNodeParameter('options', 0, {}) as any;

                        // Check if sync mode is enabled (for AI Agent compatibility)
                        const syncMode = options.syncMode === true;
						
						if (syncMode) {
							// Sync mode: Poll for responses instead of using putExecutionToWait
							// This is necessary for AI Agents which don't support async/await properly
							const syncTimeout = options.syncTimeout || 7200; // Default 2 hours
							const pollInterval = options.pollInterval || 5; // Default 5 seconds

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

							const sendResponse = await omnaraApiRequest.call(
								this,
								'POST',
								'/messages/agent',
								body,
							);

							const messageId = sendResponse.message_id;

							// Now poll for responses
							const startTime = Date.now();
							const timeoutMs = syncTimeout * 1000;
							const lastReadMessageId = messageId;

							while (Date.now() - startTime < timeoutMs) {
								// Wait for poll interval using Date.now()
								// Community nodes must avoid restricted globals
								const pollStart = Date.now();
								const pollMs = pollInterval * 1000;
								while (Date.now() - pollStart < pollMs) {
									// Small delay to avoid blocking
									await new Promise((resolve) => resolve(undefined));
								}

								// Check for pending messages
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

									// Check if we got any user messages
									if (pendingResponse.messages && pendingResponse.messages.length > 0) {
										// Return the last user message as the result
										const lastMessage =
											pendingResponse.messages[pendingResponse.messages.length - 1];

										// Return the response in a format compatible with the workflow
										return [
											[
												{
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
												},
											],
										];
									}

									// If status is 'stale', continue polling
									if (pendingResponse.status === 'stale') {
										// We might need to re-fetch to get the current state
										// For now, continue polling
									}
								} catch (pollError) {
									// Continue polling on error
								}
							}

							// Timeout reached without response
							return [
								[
									{
										json: {
											success: false,
											agent_instance_id: agentInstanceId,
											error: 'Timeout waiting for user response',
											timeout_seconds: syncTimeout,
										},
										pairedItem: i,
									},
								],
							];
						} else {
							// Original async mode with putExecutionToWait
							// Get the webhook URL from n8n's execution context
							// This automatically provides the correct URL for any n8n instance
							const resumeUrl = this.evaluateExpression('{{ $execution?.resumeUrl }}', 0) as string;
							const nodeId = this.evaluateExpression('{{ $nodeId }}', 0) as string;
							const executionId = this.getExecutionId();
							
							// Construct the full webhook URL with node ID appended
							const webhookUrl = `${resumeUrl}/${nodeId}`;

							const body: any = {
								agent_instance_id: agentInstanceId,
								agent_type: agentType,
								content: messageContent,
								requires_user_input: true,
								// Send webhook URL in metadata so Omnara knows where to send the response
								message_metadata: {
									webhook_url: webhookUrl, // Full URL with execution ID and node ID
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

							// Send message to Omnara (like Slack sends to chat.postMessage)
							await omnaraApiRequest.call(this, 'POST', '/messages/agent', body);

							// Configure wait time
							const waitTill = configureWaitTillDate(this, 0);

							// Put execution to wait
							if (waitTill === 'WAIT_INDEFINITELY') {
								await this.putExecutionToWait(new Date(Date.now() + 365 * 24 * 60 * 60 * 1000));
							} else {
								await this.putExecutionToWait(waitTill as Date);
							}

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
