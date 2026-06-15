import { Button } from "@/components/ui/button";
import { PlugZap, CheckCircle2, Loader2, Wrench, Server } from "lucide-react";
import { TidalIcon, QobuzIcon, AmazonIcon, AppleMusicIcon, DeezerIcon } from "./PlatformIcons";
import { useApiStatus } from "@/hooks/useApiStatus";
import { SPOTIFLAC_NEXT_SOURCES } from "@/lib/api-status";
import { openExternal } from "@/lib/utils";
function renderStatusIndicator(status: "checking" | "online" | "offline" | "idle") {
    if (status === "online") {
        return <CheckCircle2 className="h-5 w-5 text-emerald-500"/>;
    }
    if (status === "offline") {
        return <Wrench className="h-4 w-4 text-amber-600 dark:text-amber-400"/>;
    }
    return null;
}
function renderPlatformIcon(type: string) {
    if (type === "tidal") {
        return <TidalIcon className="w-5 h-5 shrink-0 text-muted-foreground"/>;
    }
    if (type === "amazon") {
        return <AmazonIcon className="w-5 h-5 shrink-0 text-muted-foreground"/>;
    }
    if (type === "deezer") {
        return <DeezerIcon className="w-5 h-5 shrink-0 text-muted-foreground"/>;
    }
    if (type === "apple") {
        return <AppleMusicIcon className="w-5 h-5 shrink-0 text-muted-foreground"/>;
    }
    return <QobuzIcon className="w-5 h-5 shrink-0 text-muted-foreground"/>;
}
export function ApiStatusTab() {
    const { sources, statuses, nextStatuses, checkingSources, checkAllCurrent, checkAllNext } = useApiStatus();
    const isCheckingCurrent = sources.some((source) => checkingSources[source.id] === true);
    const isCheckingNext = SPOTIFLAC_NEXT_SOURCES.some((source) => nextStatuses[source.id] === "checking");
    const isChecking = isCheckingCurrent || isCheckingNext;
    const checkAll = () => {
        void checkAllCurrent();
        void checkAllNext();
    };
    return (<div className="space-y-6">
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-3">
          <h3 className="text-sm font-semibold tracking-tight">SpotiFLAC</h3>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => openExternal("https://spotbye.qzz.io")} className="gap-2">
              <Server className="h-4 w-4"/>
              Details
            </Button>
            <Button variant="outline" size="sm" onClick={checkAll} disabled={isChecking} className="gap-2">
              {isChecking ? <Loader2 className="h-4 w-4 animate-spin"/> : <PlugZap className="h-4 w-4"/>}
              Check
            </Button>
          </div>
        </div>

        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {sources.map((source) => {
            const status = statuses[source.id] || "idle";
            return (<div key={source.id} className="space-y-4 p-4 border rounded-lg bg-card text-card-foreground shadow-sm">
                <div className="flex items-center justify-between gap-3">
                  <div className="flex items-center gap-3">
                    {renderPlatformIcon(source.type)}
                    <p className="font-medium leading-none">{source.name}</p>
                  </div>
                  <div className="flex items-center">{renderStatusIndicator(status)}</div>
                </div>
              </div>);
        })}
        </div>
      </div>

      <div className="border-t"/>

      <div className="space-y-4">
        <h3 className="text-sm font-semibold tracking-tight">SpotiFLAC Next</h3>

        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-5">
          {SPOTIFLAC_NEXT_SOURCES.map((source) => {
            const status = nextStatuses[source.id] || "idle";
            return (<div key={source.id} className="flex items-center justify-between p-4 border rounded-lg bg-card text-card-foreground shadow-sm">
              <div className="flex items-center gap-3">
                {renderPlatformIcon(source.id)}
                <p className="font-medium leading-none">{source.name}</p>
              </div>
              <div className="flex items-center">{renderStatusIndicator(status)}</div>
            </div>);
        })}
        </div>
      </div>
    </div>);
}
