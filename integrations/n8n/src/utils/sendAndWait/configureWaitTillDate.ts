import { IExecuteFunctions } from 'n8n-workflow';

const WAIT_INDEFINITELY = 'WAIT_INDEFINITELY';

export function configureWaitTillDate(context: IExecuteFunctions, index: number): Date | string {
	const limitWaitTime = context.getNodeParameter('limitWaitTime', index, false) as boolean;
	
	if (!limitWaitTime) {
		return WAIT_INDEFINITELY;
	}

	const limitType = context.getNodeParameter('limitType', index, 'afterTimeInterval') as string;
	
	if (limitType === 'afterTimeInterval') {
		const resumeAmount = context.getNodeParameter('resumeAmount', index, 1) as number;
		const resumeUnit = context.getNodeParameter('resumeUnit', index, 'hours') as string;
		
		let waitSeconds = 0;
		switch (resumeUnit) {
			case 'minutes':
				waitSeconds = resumeAmount * 60;
				break;
			case 'hours':
				waitSeconds = resumeAmount * 60 * 60;
				break;
			case 'days':
				waitSeconds = resumeAmount * 60 * 60 * 24;
				break;
			default:
				waitSeconds = resumeAmount;
		}
		
		const waitTill = new Date();
		waitTill.setTime(waitTill.getTime() + waitSeconds * 1000);
		return waitTill;
	} else if (limitType === 'atSpecifiedTime') {
		const maxDateAndTime = context.getNodeParameter('maxDateAndTime', index) as string;
		return new Date(maxDateAndTime);
	}
	
	return WAIT_INDEFINITELY;
}