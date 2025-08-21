import * as send from './send.operation';
import * as sendAndWait from './sendAndWait.operation';

import { INodeProperties } from 'n8n-workflow';

export { send, sendAndWait };

export const descriptions: INodeProperties[] = [
	{
		displayName: 'Operation',
		name: 'operation',
		type: 'options',
		noDataExpression: true,
		displayOptions: {
			show: {
				resource: ['message'],
			},
		},
		options: [
			{
				name: 'Send',
				value: 'send',
				description:
					'Send a non-blocking message to the user (for status updates, progress reports, or sharing your thought process). Use this when you want to inform the user but do NOT need their response to continue.',
				action: 'Send a status message',
			},
			{
				name: 'Send and Wait',
				value: 'sendAndWait',
				description:
					"Send a message and WAIT for the user's response (for questions, approvals, or conversations). Use this when you NEED the user's input to proceed with your task.",
				action: 'Ask user a question and wait',
			},
		],
		default: 'send',
	},
	...send.sendMessageDescription,
	...sendAndWait.sendAndWaitDescription,
];
