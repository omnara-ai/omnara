import type { ComponentProps } from 'react'

export {
  ArrowDownIcon,
  ArrowUpRightIcon as ArrowUpRight,
  BookOpenIcon as BookOpen,
  CpuChipIcon as Bot,
  CpuChipIcon as BotIcon,
  LightBulbIcon as Brain,
  CpuChipIcon as BrainCircuit,
  BuildingOffice2Icon as Building2,
  CheckIcon as Check,
  CheckIcon,
  ChevronDownIcon as ChevronDown,
  ChevronDownIcon,
  ChevronLeftIcon,
  ChevronRightIcon as ChevronRight,
  ChevronRightIcon,
  ChevronUpDownIcon as ChevronsUpDown,
  ChevronUpDownIcon as ChevronsUpDownIcon,
  ChevronUpIcon,
  ExclamationCircleIcon as CircleAlert,
  CheckCircleIcon as CircleCheck,
  QuestionMarkCircleIcon as CircleHelp,
  DocumentDuplicateIcon as Copy,
  DocumentDuplicateIcon as CopyIcon,
  CreditCardIcon as CreditCard,
  EllipsisHorizontalIcon as Ellipsis,
  ArchiveBoxIcon as FileArchive,
  FingerPrintIcon as Fingerprint,
  FolderIcon as Folder,
  HomeIcon as House,
  InformationCircleIcon as InfoIcon,
  KeyIcon as KeyRound,
  ArrowPathIcon as Loader2,
  ArrowPathIcon as LoaderCircleIcon,
  ArrowRightStartOnRectangleIcon as LogOut,
  EnvelopeIcon as Mail,
  EnvelopeOpenIcon as MailCheck,
  ChatBubbleOvalLeftEllipsisIcon as MessageCircleQuestion,
  ComputerDesktopIcon as Monitor,
  MoonIcon as Moon,
  EllipsisHorizontalIcon as MoreHorizontal,
  EllipsisHorizontalIcon as MoreHorizontalIcon,
  Bars3Icon as PanelLeftIcon,
  PaperClipIcon as Paperclip,
  PencilIcon,
  PlusIcon as Plus,
  PlusIcon,
  MagnifyingGlassIcon as SearchIcon,
  PaperAirplaneIcon as SendHorizontal,
  ServerIcon as Server,
  Cog6ToothIcon as SettingsIcon,
  SparklesIcon as Sparkles,
  StopIcon as Square,
  SunIcon as Sun,
  CommandLineIcon as Terminal,
  TrashIcon as Trash2,
  TrashIcon as Trash2Icon,
  ExclamationTriangleIcon as TriangleAlert,
  ArrowUpTrayIcon as Upload,
  UsersIcon as Users,
  XMarkIcon as X,
  XMarkIcon as XIcon,
} from '@heroicons/react/24/outline'

function CircleBase({
  dashed,
  ...props
}: ComponentProps<'svg'> & {
  dashed?: boolean
}) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeDasharray={dashed ? '3 3' : undefined}
      aria-hidden="true"
      {...props}
    >
      <circle cx="12" cy="12" r="9" />
    </svg>
  )
}

export function Circle(props: ComponentProps<'svg'>) {
  return <CircleBase {...props} />
}

export function CircleIcon(props: ComponentProps<'svg'>) {
  return <CircleBase {...props} />
}

export function CircleDashed(props: ComponentProps<'svg'>) {
  return <CircleBase dashed {...props} />
}
