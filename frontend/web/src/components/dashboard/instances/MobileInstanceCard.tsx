import { Link } from "react-router-dom";
import { formatDistanceToNow } from "date-fns";
import { ChevronRight, MessageCircle, AlertCircle } from "lucide-react";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { AgentInstance, AgentStatus } from "@/types/dashboard";
import { cn } from "@/lib/utils";
import { getStatusIcon, getStatusColor, getStatusLabel } from "@/utils/statusUtils";

interface MobileInstanceCardProps {
  instance: AgentInstance;
  onClick?: () => void;
}

export function MobileInstanceCard({ instance, onClick }: MobileInstanceCardProps) {
  const isWaitingForInput = instance.status === AgentStatus.AWAITING_INPUT;

  return (
    <Card 
      className={cn(
        "bg-white/10 backdrop-blur-md border border-white/20 hover:bg-white/15 transition-all cursor-pointer",
        isWaitingForInput && "border-yellow-500/30"
      )}
      onClick={onClick}
    >
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between gap-2">
          <div className="flex-1 min-w-0">
            <h3 className="font-semibold text-sm truncate text-white">
              {instance.agent_type_name || "Unknown Agent"}
            </h3>
            <p className="text-xs text-off-white/60 mt-1">
              Started {formatDistanceToNow(new Date(instance.started_at), { addSuffix: true })}
            </p>
          </div>
          <Badge variant="secondary" className={cn("shrink-0", getStatusColor(instance.status))}>
            {getStatusLabel(instance.status, instance.last_signal_at)}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        <div className="space-y-3">
          {/* Latest Message */}
          {instance.latest_message && (
            <div className="text-sm text-off-white/80">
              <p className="line-clamp-2">{instance.latest_message}</p>
            </div>
          )}

          {/* Stats Row */}
          <div className="flex items-center justify-between text-xs">
            <div className="flex items-center gap-3">
              <span className="text-off-white/60">
                {instance.chat_length || 0} messages
              </span>
            </div>

            {isWaitingForInput && (
              <Badge variant="destructive" className="text-xs bg-yellow-500/20 border-yellow-400/30 text-yellow-200">
                <AlertCircle className="h-3 w-3 mr-1" />
                Awaiting input
              </Badge>
            )}
          </div>

          {/* Action Button */}
          <Link to={`/dashboard/instances/${instance.id}`} onClick={(e) => e.stopPropagation()}>
            <Button 
              size="sm" 
              variant="outline"
              className="w-full border-electric-accent/50 text-electric-accent hover:bg-electric-accent/10"
            >
              {isWaitingForInput ? "Respond" : "View Details"}
              <ChevronRight className="h-4 w-4 ml-2" />
            </Button>
          </Link>
        </div>
      </CardContent>
    </Card>
  );
}