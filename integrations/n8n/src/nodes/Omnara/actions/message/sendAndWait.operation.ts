import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeProperties,
	NodeOperationError,
} from 'n8n-workflow';

import { omnaraApiRequest, formatMessageResponse } from '../../../../utils/GenericFunctions';

// This matches Discord's approach - properties for the sendAndWait message
export const sendAndWaitDescription: INodeProperties[] = [
	{
		displayName: 'Agent Instance ID',
		name: 'agentInstanceId',
		type: 'string',
		default: '',
		required: true,
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
			},
		},
		description: 'The ID of the agent instance to send the message to',
	},
	{
		displayName: 'Message',
		name: 'message',
		type: 'string',
		default: '',
		required: true,
		typeOptions: {
			rows: 4,
		},
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
			},
		},
		placeholder: 'e.g. Please review and approve this deployment',
		description: 'The message to send to the user for approval or response',
	},
	{
		displayName: 'Subject',
		name: 'subject',
		type: 'string',
		default: '',
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
			},
		},
		placeholder: 'e.g. Deployment Approval Required',
		description: 'Subject line for the notification',
	},
	{
		displayName: 'Options',
		name: 'options',
		type: 'collection',
		placeholder: 'Add Option',
		default: {},
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
			},
		},
		options: [
			{
				displayName: 'Agent Type',
				name: 'agentType',
				type: 'string',
				default: '',
				description: 'Type of agent (e.g., "claude_code", "cursor")',
			},
			{
				displayName: 'Webhook URL',
				name: 'webhookUrl',
				type: 'string',
				default: '',
				description: 'Webhook URL from Omnara Wait node (use {{$execution.resumeUrl}} from the Wait node)',
				placeholder: 'e.g. {{$execution.resumeUrl}}',
			},
			{
				displayName: 'Send Email',
				name: 'sendEmail',
				type: 'boolean',
				default: true,
				description: 'Whether to send an email notification',
			},
			{
				displayName: 'Send SMS',
				name: 'sendSms',
				type: 'boolean',
				default: false,
				description: 'Whether to send an SMS notification',
			},
			{
				displayName: 'Send Push',
				name: 'sendPush',
				type: 'boolean',
				default: true,
				description: 'Whether to send a push notification',
			},
		],
	},
	{
		displayName: 'Limit Wait Time',
		name: 'limitWaitTime',
		type: 'boolean',
		default: false,
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
			},
		},
		description: 'Whether to set a time limit for how long to wait for a response',
	},
	{
		displayName: 'Limit Type',
		name: 'limitType',
		type: 'options',
		default: 'afterTimeInterval',
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
				limitWaitTime: [true],
			},
		},
		options: [
			{
				name: 'After Time Interval',
				value: 'afterTimeInterval',
				description: 'Wait for a specified amount of time',
			},
			{
				name: 'At Specified Time',
				value: 'atSpecifiedTime',
				description: 'Wait until a specific date and time',
			},
		],
		description: 'When to stop waiting if no response is received',
	},
	{
		displayName: 'Resume After',
		name: 'resumeAmount',
		type: 'number',
		default: 1,
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
				limitWaitTime: [true],
				limitType: ['afterTimeInterval'],
			},
		},
		description: 'Amount of time to wait',
		typeOptions: {
			minValue: 0,
		},
	},
	{
		displayName: 'Unit',
		name: 'resumeUnit',
		type: 'options',
		default: 'hours',
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
				limitWaitTime: [true],
				limitType: ['afterTimeInterval'],
			},
		},
		options: [
			{
				name: 'Minutes',
				value: 'minutes',
			},
			{
				name: 'Hours',
				value: 'hours',
			},
			{
				name: 'Days',
				value: 'days',
			},
		],
		description: 'Unit of time to wait',
	},
	{
		displayName: 'Max Date and Time',
		name: 'maxDateAndTime',
		type: 'dateTime',
		default: '',
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
				limitWaitTime: [true],
				limitType: ['atSpecifiedTime'],
			},
		},
		description: 'The date and time to stop waiting for a response',
	},
];

// This matches Discord's approach - just send the message and return
export async function execute(
	this: IExecuteFunctions,
	items: INodeExecutionData[],
): Promise<INodeExecutionData[]> {
	const returnData: INodeExecutionData[] = [];

	for (let i = 0; i < items.length; i++) {
		try {
			const agentInstanceId = this.getNodeParameter('agentInstanceId', i) as string;
			const message = this.getNodeParameter('message', i) as string;
			const subject = this.getNodeParameter('subject', i, '') as string;
			const options = this.getNodeParameter('options', i, {}) as any;

			if (!agentInstanceId) {
				throw new NodeOperationError(
					this.getNode(),
					'Agent Instance ID is required',
					{ itemIndex: i },
				);
			}

			if (!message) {
				throw new NodeOperationError(
					this.getNode(),
					'Message is required',
					{ itemIndex: i },
				);
			}

			// Build the message content
			const content = subject ? `${subject}\n\n${message}` : message;
			
			// Create the message body - this will require user input
			const body: any = {
				agent_instance_id: agentInstanceId,
				content,
				requires_user_input: true, // Mark as requiring user response
				agent_type: options.agentType,
				send_email: options.sendEmail !== false,
				send_sms: options.sendSms,
				send_push: options.sendPush !== false,
			};

			// Get the webhook URL that will be used to resume the workflow
			// For now, just use the one from options if provided
			const webhookUrl = options.webhookUrl || '';
			
			// If webhook URL is available, store it in message metadata
			if (webhookUrl) {
				body.message_metadata = {
					webhook_url: webhookUrl,
				};
			}

			// Send the message to Omnara
			const response = await omnaraApiRequest.call(
				this,
				'POST',
				'/messages/agent',
				body,
			);

			// Return the response - just like Discord does
			// The actual waiting happens in n8n's workflow with a Wait node
			returnData.push({
				json: {
					success: true,
					agentInstanceId: response.agent_instance_id,
					messageId: response.message_id,
					status: 'sent',
					message: webhookUrl 
						? 'Message sent with webhook URL. Waiting for user response...'
						: 'Message sent. Use with Omnara Webhook node to wait for response.',
					webhookUrlProvided: !!webhookUrl,
					queuedUserMessages: response.queued_user_messages 
						? response.queued_user_messages.map(formatMessageResponse)
						: [],
				},
				pairedItem: i,
			});
		} catch (error) {
			if (this.continueOnFail()) {
				returnData.push({
					json: {
						error: error instanceof Error ? error.message : String(error),
					},
					pairedItem: i,
				});
				continue;
			}
			throw error;
		}
	}

	return returnData;
}