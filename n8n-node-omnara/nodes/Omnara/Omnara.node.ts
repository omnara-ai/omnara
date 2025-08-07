import type {
	IExecuteFunctions,
	INodeExecutionData,
	INodeType,
	INodeTypeDescription,
} from 'n8n-workflow';

import { NodeOperationError } from 'n8n-workflow';

import {
	omnaraApiRequest,
	sendAndWaitForApproval,
	sendMessage,
	endSession,
} from './GenericFunctions';

export class Omnara implements INodeType {
	description: INodeTypeDescription = {
		displayName: 'Omnara',
		name: 'omnara',
		icon: 'file:omnara.svg',
		group: ['transform'],
		version: 1,
		subtitle: '={{$parameter["operation"]}}',
		description: 'Human-in-the-loop approvals and notifications via Omnara',
		defaults: {
			name: 'Omnara',
		},
		inputs: ['main'],
		outputs: ['main'],
		credentials: [
			{
				name: 'omnaraApi',
				required: true,
			},
		],
		properties: [
			{
				displayName: 'Operation',
				name: 'operation',
				type: 'options',
				noDataExpression: true,
				options: [
					{
						name: 'Send and Wait for Approval',
						value: 'sendAndWait',
						description: 'Send a message and wait for human response (approval gate)',
						action: 'Send message and wait for approval',
					},
					{
						name: 'Send Message',
						value: 'sendMessage',
						description: 'Send a notification without waiting',
						action: 'Send a message',
					},
					{
						name: 'End Session',
						value: 'endSession',
						description: 'Mark the agent session as completed',
						action: 'End session',
					},
				],
				default: 'sendAndWait',
			},

			// Fields for Send and Wait for Approval
			{
				displayName: 'Message',
				name: 'message',
				type: 'string',
				typeOptions: {
					rows: 4,
				},
				default: '',
				placeholder: 'Should I proceed with this action?\n\nDetails: {{$json["details"]}}',
				description: 'The message or question to send for approval',
				required: true,
				displayOptions: {
					show: {
						operation: ['sendAndWait'],
					},
				},
			},
			{
				displayName: 'Agent Type',
				name: 'agentType',
				type: 'string',
				default: 'n8n_workflow',
				description: 'Type/name of the agent (e.g., "approval_bot", "workflow_assistant")',
				displayOptions: {
					show: {
						operation: ['sendAndWait', 'sendMessage'],
					},
				},
			},
			{
				displayName: 'Agent Instance ID',
				name: 'agentInstanceId',
				type: 'string',
				default: '',
				description: 'Optional: Specific instance ID to continue an existing session. Leave empty to auto-generate.',
				displayOptions: {
					show: {
						operation: ['sendAndWait', 'sendMessage'],
					},
				},
			},
			{
				displayName: 'Additional Options',
				name: 'additionalOptions',
				type: 'collection',
				placeholder: 'Add Option',
				default: {},
				displayOptions: {
					show: {
						operation: ['sendAndWait'],
					},
				},
				options: [
					{
						displayName: 'Timeout (Minutes)',
						name: 'timeout',
						type: 'number',
						default: 60,
						description: 'How long to wait for a response in minutes',
					},
					{
						displayName: 'Poll Interval (Seconds)',
						name: 'pollInterval',
						type: 'number',
						default: 10,
						description: 'How often to check for responses in seconds',
					},
					{
						displayName: 'Approval Keywords',
						name: 'approvalKeywords',
						type: 'string',
						default: 'yes,approve,ok,continue,proceed,confirmed,accept',
						description: 'Comma-separated keywords that indicate approval',
					},
					{
						displayName: 'Rejection Keywords',
						name: 'rejectionKeywords',
						type: 'string',
						default: 'no,reject,stop,cancel,deny,decline,abort',
						description: 'Comma-separated keywords that indicate rejection',
					},
					{
						displayName: 'Send Push Notification',
						name: 'sendPush',
						type: 'boolean',
						default: true,
						description: 'Whether to send a push notification',
					},
					{
						displayName: 'Send Email',
						name: 'sendEmail',
						type: 'boolean',
						default: false,
						description: 'Whether to send an email notification',
					},
					{
						displayName: 'Send SMS',
						name: 'sendSms',
						type: 'boolean',
						default: false,
						description: 'Whether to send an SMS notification',
					},
				],
			},

			// Fields for Send Message (no wait)
			{
				displayName: 'Message',
				name: 'simpleMessage',
				type: 'string',
				typeOptions: {
					rows: 3,
				},
				default: '',
				placeholder: 'Processing item {{$json["id"]}}...',
				description: 'The notification message to send',
				required: true,
				displayOptions: {
					show: {
						operation: ['sendMessage'],
					},
				},
			},

			// Fields for End Session
			{
				displayName: 'Agent Instance ID',
				name: 'endInstanceId',
				type: 'string',
				default: '={{$node["Omnara"].json["agentInstanceId"]}}',
				description: 'The agent instance ID to end. Usually from a previous Omnara node.',
				required: true,
				displayOptions: {
					show: {
						operation: ['endSession'],
					},
				},
			},
		],
	};

	async execute(this: IExecuteFunctions): Promise<INodeExecutionData[][]> {
		const items = this.getInputData();
		const returnData: INodeExecutionData[] = [];
		const operation = this.getNodeParameter('operation', 0) as string;

		for (let i = 0; i < items.length; i++) {
			try {
				let responseData: any;

				if (operation === 'sendAndWait') {
					// Send and Wait for Approval
					const message = this.getNodeParameter('message', i) as string;
					const agentType = this.getNodeParameter('agentType', i) as string;
					const agentInstanceId = this.getNodeParameter('agentInstanceId', i) as string;
					const additionalOptions = this.getNodeParameter('additionalOptions', i) as any;

					responseData = await sendAndWaitForApproval.call(
						this,
						message,
						agentType,
						agentInstanceId,
						additionalOptions,
					);

					// Parse the response to determine approval status
					const response = responseData.response || responseData.queued_user_messages?.[0] || '';
					const approvalKeywords = (additionalOptions.approvalKeywords || 'yes,approve,ok,continue,proceed,confirmed,accept')
						.toLowerCase()
						.split(',')
						.map((k: string) => k.trim());
					const rejectionKeywords = (additionalOptions.rejectionKeywords || 'no,reject,stop,cancel,deny,decline,abort')
						.toLowerCase()
						.split(',')
						.map((k: string) => k.trim());

					const responseLower = response.toLowerCase();
					const isApproved = approvalKeywords.some((keyword: string) => responseLower.includes(keyword));
					const isRejected = rejectionKeywords.some((keyword: string) => responseLower.includes(keyword));

					// Add approval status to response
					responseData = {
						...responseData,
						approved: isApproved && !isRejected,
						response: response,
						agentInstanceId: responseData.agent_instance_id,
					};

				} else if (operation === 'sendMessage') {
					// Send Message (no wait)
					const message = this.getNodeParameter('simpleMessage', i) as string;
					const agentType = this.getNodeParameter('agentType', i) as string;
					const agentInstanceId = this.getNodeParameter('agentInstanceId', i) as string;

					responseData = await sendMessage.call(
						this,
						message,
						agentType,
						agentInstanceId,
					);

					responseData = {
						...responseData,
						agentInstanceId: responseData.agent_instance_id,
					};

				} else if (operation === 'endSession') {
					// End Session
					const agentInstanceId = this.getNodeParameter('endInstanceId', i) as string;

					if (!agentInstanceId) {
						throw new NodeOperationError(
							this.getNode(),
							'Agent Instance ID is required to end a session',
							{ itemIndex: i },
						);
					}

					responseData = await endSession.call(this, agentInstanceId);

				} else {
					throw new NodeOperationError(
						this.getNode(),
						`Unknown operation: ${operation}`,
						{ itemIndex: i },
					);
				}

				returnData.push({
					json: responseData,
					pairedItem: { item: i },
				});

			} catch (error) {
				if (this.continueOnFail()) {
					returnData.push({
						json: {
							error: error.message,
						},
						pairedItem: { item: i },
					});
					continue;
				}
				throw error;
			}
		}

		return [returnData];
	}
}