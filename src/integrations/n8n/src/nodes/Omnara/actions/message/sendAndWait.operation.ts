import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeProperties,
	NodeOperationError,
} from 'n8n-workflow';

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
		placeholder: 'e.g. 550e8400-e29b-41d4-a716-446655440000',
		description:
			'A unique identifier for this workflow run. Must be the same across all Omnara nodes in this workflow. Use webhook data or generate with {{ $uuid() }}',
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
		placeholder: 'e.g. Customer Support',
		description:
			'The name of your agent on Omnara dashboard. Must be the same across all Omnara nodes',
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
		description:
			'Question or request that requires a user response. This will appear in their Omnara web/mobile app and the workflow will pause until they respond. Use this for questions, approvals, or when you need human input to continue',
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
				description:
					'Whether to enable AI Agent compatibility mode. When enabled, the node will wait for a response before continuing. Required when using this node as an AI Agent tool',
			},
			{
				displayName: 'Sync Timeout (Seconds)',
				name: 'syncTimeout',
				type: 'number',
				default: 7200,
				displayOptions: {
					show: {
						syncMode: [true],
					},
				},
				description:
					'Maximum time to wait for a response in sync mode (in seconds)',
				typeOptions: {
					minValue: 10,
					maxValue: 172800,
				},
			},
			{
				displayName: 'Poll Interval (Seconds)',
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
					minValue: 5,
					maxValue: 60,
				},
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
	_items: INodeExecutionData[],
): Promise<INodeExecutionData[][]> {
	// Should not be called - handled in main node
	throw new NodeOperationError(
		this.getNode(),
		'sendAndWait should be handled in the main node file',
	);
}
