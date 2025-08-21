import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeProperties,
	NodeOperationError,
	SEND_AND_WAIT_OPERATION,
} from 'n8n-workflow';

import { omnaraApiRequest, formatMessageResponse } from '../../../../utils/GenericFunctions';
import { configureWaitTillDate } from '../../../../utils/sendAndWait/configureWaitTillDate';

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
		displayName: 'Agent Type',
		name: 'agentType',
		type: 'string',
		default: '',
		required: true,
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['sendAndWait'],
			},
		},
		placeholder: 'e.g. claude_code, cursor',
		description: 'Type of agent (e.g., "claude_code", "cursor"). Required when creating a new instance.',
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
				displayName: 'Sync Mode (For AI Agents)',
				name: 'syncMode',
				type: 'boolean',
				default: false,
				description: 'Enable synchronous mode for AI Agent compatibility. When enabled, the node will poll for responses instead of using async wait. Required when using this node as an AI Agent tool.',
			},
			{
				displayName: 'Sync Timeout (seconds)',
				name: 'syncTimeout',
				type: 'number',
				default: 300,
				displayOptions: {
					show: {
						syncMode: [true],
					},
				},
				description: 'Maximum time to wait for response in sync mode (in seconds). Default is 5 minutes, max is 2 hours.',
				typeOptions: {
					minValue: 10,
					maxValue: 7200,
				},
			},
			{
				displayName: 'Poll Interval (seconds)',
				name: 'pollInterval',
				type: 'number',
				default: 5,
				displayOptions: {
					show: {
						syncMode: [true],
					},
				},
				description: 'How often to check for responses in sync mode (in seconds)',
				typeOptions: {
					minValue: 1,
					maxValue: 60,
				},
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

// This function is not used - sendAndWait is handled in the main node file
// following the Slack pattern
export async function execute(
	this: IExecuteFunctions,
	items: INodeExecutionData[],
): Promise<INodeExecutionData[][]> {
	// Should not be called - handled in main node
	throw new NodeOperationError(
		this.getNode(),
		'sendAndWait should be handled in the main node file',
	);
}