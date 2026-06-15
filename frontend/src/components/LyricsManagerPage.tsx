import { useState, useCallback, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { Upload, X, FileText, Trash2, AlertCircle, Music, Clock, Download } from "lucide-react";
import { Spinner } from "@/components/ui/spinner";
import { ReadEmbeddedLyrics, SelectLyricsFiles, ExtractLyricsToLRC } from "../../wailsjs/go/main/App";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { OnFileDrop, OnFileDropOff } from "../../wailsjs/runtime/runtime";
interface LyricsFile {
    path: string;
    name: string;
    format: string;
    lyrics: string;
    source: string;
    synced: boolean;
    status: "loading" | "loaded" | "empty" | "error";
    error?: string;
}
const SUPPORTED_EXTENSIONS = [".lrc", ".txt", ".flac", ".mp3", ".m4a", ".aac", ".opus", ".ogg"];
function getExtension(path: string): string {
    const lower = path.toLowerCase();
    const dot = lower.lastIndexOf(".");
    return dot >= 0 ? lower.slice(dot) : "";
}
export function LyricsManagerPage() {
    const [files, setFiles] = useState<LyricsFile[]>([]);
    const [selectedPath, setSelectedPath] = useState<string | null>(null);
    const [isDragging, setIsDragging] = useState(false);
    const [isFullscreen, setIsFullscreen] = useState(false);
    const [extracting, setExtracting] = useState(false);
    useEffect(() => {
        const checkFullscreen = () => {
            setIsFullscreen(window.innerHeight >= window.screen.height * 0.9);
        };
        checkFullscreen();
        window.addEventListener("resize", checkFullscreen);
        window.addEventListener("focus", checkFullscreen);
        return () => {
            window.removeEventListener("resize", checkFullscreen);
            window.removeEventListener("focus", checkFullscreen);
        };
    }, []);
    const addFiles = useCallback(async (paths: string[]) => {
        const validPaths = paths.filter((path) => SUPPORTED_EXTENSIONS.includes(getExtension(path)));
        if (validPaths.length === 0) {
            if (paths.length > 0) {
                toast.error("Unsupported files", {
                    description: "Only LRC and audio files (FLAC, MP3, M4A) are supported.",
                });
            }
            return;
        }
        const newPaths: string[] = [];
        setFiles((prev) => {
            const toAdd = validPaths.filter((path) => !prev.some((f) => f.path === path));
            newPaths.push(...toAdd);
            const entries: LyricsFile[] = toAdd.map((path) => {
                const name = path.split(/[/\\]/).pop() || path;
                return {
                    path,
                    name,
                    format: getExtension(path).slice(1),
                    lyrics: "",
                    source: "",
                    synced: false,
                    status: "loading" as const,
                };
            });
            if (entries.length === 0) {
                return prev;
            }
            return [...prev, ...entries];
        });
        for (const path of newPaths) {
            try {
                const result = await ReadEmbeddedLyrics(path);
                setFiles((prev) => prev.map((f) => {
                    if (f.path !== path)
                        return f;
                    if (result.error) {
                        return { ...f, status: "empty" as const, error: result.error };
                    }
                    return {
                        ...f,
                        lyrics: result.lyrics,
                        source: result.source,
                        synced: result.synced,
                        status: "loaded" as const,
                    };
                }));
            }
            catch (err) {
                setFiles((prev) => prev.map((f) => f.path === path
                    ? { ...f, status: "error" as const, error: err instanceof Error ? err.message : "Failed to read lyrics" }
                    : f));
            }
        }
        setSelectedPath((prev) => prev ?? newPaths[0] ?? null);
    }, []);
    const handleSelectFiles = async () => {
        try {
            const selected = await SelectLyricsFiles();
            if (selected && selected.length > 0) {
                addFiles(selected);
            }
        }
        catch (err) {
            toast.error("File Selection Failed", {
                description: err instanceof Error ? err.message : "Failed to select files",
            });
        }
    };
    const handleFileDrop = useCallback((_x: number, _y: number, paths: string[]) => {
        setIsDragging(false);
        if (paths.length === 0)
            return;
        addFiles(paths);
    }, [addFiles]);
    useEffect(() => {
        OnFileDrop((x, y, paths) => {
            handleFileDrop(x, y, paths);
        }, true);
        return () => {
            OnFileDropOff();
        };
    }, [handleFileDrop]);
    const removeFile = (path: string) => {
        setFiles((prev) => {
            const next = prev.filter((f) => f.path !== path);
            setSelectedPath((current) => {
                if (current !== path)
                    return current;
                return next[0]?.path ?? null;
            });
            return next;
        });
    };
    const clearFiles = () => {
        setFiles([]);
        setSelectedPath(null);
    };
    const selectedFile = files.find((f) => f.path === selectedPath) || null;
    const extractFile = async (file: LyricsFile, overwrite: boolean) => {
        const result = await ExtractLyricsToLRC(file.path, overwrite);
        if (result.success) {
            return { ok: true as const, output: result.output_path };
        }
        if (result.already_exists) {
            return { ok: false as const, alreadyExists: true, output: result.output_path };
        }
        return { ok: false as const, error: result.error || "Failed to extract lyrics" };
    };
    const handleExtractSelected = async () => {
        if (!selectedFile || selectedFile.status !== "loaded")
            return;
        setExtracting(true);
        try {
            const result = await extractFile(selectedFile, false);
            if (result.ok) {
                toast.success("Lyrics extracted", { description: result.output });
            }
            else if (result.alreadyExists) {
                toast.info("LRC already exists", {
                    description: "A .lrc file with the same name already exists next to this file.",
                });
            }
            else {
                toast.error("Extract failed", { description: result.error });
            }
        }
        catch (err) {
            toast.error("Extract failed", {
                description: err instanceof Error ? err.message : "Unknown error",
            });
        }
        finally {
            setExtracting(false);
        }
    };
    const handleExtractAll = async () => {
        const extractable = files.filter((f) => f.status === "loaded");
        if (extractable.length === 0) {
            toast.error("Nothing to extract", {
                description: "No files with embedded lyrics are loaded.",
            });
            return;
        }
        setExtracting(true);
        let success = 0;
        let skipped = 0;
        let failed = 0;
        for (const file of extractable) {
            try {
                const result = await extractFile(file, false);
                if (result.ok)
                    success++;
                else if (result.alreadyExists)
                    skipped++;
                else
                    failed++;
            }
            catch {
                failed++;
            }
        }
        setExtracting(false);
        if (success > 0) {
            toast.success("Lyrics extracted", {
                description: `${success} file(s) extracted${skipped > 0 ? `, ${skipped} skipped` : ""}${failed > 0 ? `, ${failed} failed` : ""}`,
            });
        }
        else if (skipped > 0 && failed === 0) {
            toast.info("Already extracted", {
                description: `${skipped} .lrc file(s) already exist.`,
            });
        }
        else {
            toast.error("Extract failed", {
                description: `${failed} file(s) failed to extract.`,
            });
        }
    };
    const embeddedLoadedCount = files.filter((f) => f.status === "loaded" && f.source === "embedded").length;
    return (<div className={`space-y-6 ${isFullscreen ? "h-full flex flex-col" : ""}`}>
        <div className="flex items-center justify-between">
            <h1 className="text-2xl font-bold">Lyrics Manager</h1>
            {files.length > 0 && (<div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={handleSelectFiles}>
                    <Upload className="h-4 w-4"/>
                    Add Files
                </Button>
                {embeddedLoadedCount > 0 && (<Button variant="outline" size="sm" onClick={handleExtractAll} disabled={extracting}>
                    {extracting ? <Spinner className="h-4 w-4"/> : <Download className="h-4 w-4"/>}
                    Extract All
                </Button>)}
                <Button variant="outline" size="sm" onClick={clearFiles} disabled={extracting}>
                    <Trash2 className="h-4 w-4"/>
                    Clear All
                </Button>
            </div>)}
        </div>

        <div className={`flex flex-col items-center justify-center border-2 border-dashed rounded-lg transition-all ${isFullscreen ? "flex-1 min-h-[400px]" : "min-h-[400px]"} ${isDragging
            ? "border-primary bg-primary/10"
            : "border-muted-foreground/30"}`} onDragOver={(e) => {
            e.preventDefault();
            setIsDragging(true);
        }} onDragLeave={(e) => {
            e.preventDefault();
            setIsDragging(false);
        }} onDrop={(e) => {
            e.preventDefault();
            setIsDragging(false);
        }} style={{ "--wails-drop-target": "drop" } as React.CSSProperties}>
            {files.length === 0 ? (<>
                <div className="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-muted">
                    <Upload className="h-8 w-8 text-primary"/>
                </div>
                <p className="text-sm text-muted-foreground mb-4 text-center">
                    {isDragging
                ? "Drop your files here"
                : "Drag and drop LRC or audio files here, or click the button below to select"}
                </p>
                <Button onClick={handleSelectFiles} size="lg">
                    <Upload className="h-5 w-5"/>
                    Select Files
                </Button>
                <p className="text-xs text-muted-foreground mt-4 text-center">
                    Reads embedded lyrics from FLAC, MP3, M4A, Opus or plain LRC files
                </p>
            </>) : (<div className="w-full h-full p-4 flex flex-col md:flex-row gap-4 min-h-0">

                <div className="md:w-64 shrink-0 flex flex-col gap-2 md:border-r md:pr-4 max-h-48 md:max-h-none overflow-y-auto">
                    {files.map((file) => {
                const isActive = file.path === selectedPath;
                return (<button key={file.path} onClick={() => setSelectedPath(file.path)} className={`group flex items-center gap-2 rounded-lg border p-2 text-left transition-colors ${isActive ? "border-primary bg-primary/10" : "hover:bg-muted/60"}`}>
                            {file.status === "loading" ? (<Spinner className="h-4 w-4 shrink-0 text-primary"/>)
                        : file.status === "error" || file.status === "empty" ? (<AlertCircle className="h-4 w-4 shrink-0 text-destructive"/>)
                            : (<FileText className="h-4 w-4 shrink-0 text-muted-foreground"/>)}
                            <div className="flex-1 min-w-0">
                                <p className="truncate text-xs font-medium">{file.name}</p>
                                <p className="truncate text-[10px] uppercase text-muted-foreground">{file.format}</p>
                            </div>
                            <span role="button" tabIndex={-1} onClick={(e) => { e.stopPropagation(); removeFile(file.path); }} className="opacity-0 group-hover:opacity-100 transition-opacity rounded p-1 hover:bg-muted">
                                <X className="h-3.5 w-3.5"/>
                            </span>
                        </button>);
            })}
                </div>

                <div className="flex-1 min-w-0 flex flex-col min-h-0">
                    {!selectedFile ? (<div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
                        Select a file to view its lyrics
                    </div>) : selectedFile.status === "loading" ? (<div className="flex-1 flex items-center justify-center gap-2 text-sm text-muted-foreground">
                        <Spinner className="h-4 w-4"/>
                        Reading lyrics...
                    </div>) : selectedFile.status === "error" || selectedFile.status === "empty" ? (<div className="flex-1 flex flex-col items-center justify-center gap-2 text-center px-6">
                        <AlertCircle className="h-8 w-8 text-destructive"/>
                        <p className="text-sm font-medium">{selectedFile.name}</p>
                        <p className="text-xs text-muted-foreground">{selectedFile.error || "No lyrics found"}</p>
                    </div>) : (<>
                        <div className="flex flex-col gap-2 pb-3 border-b shrink-0">
                            <div className="flex items-center gap-2 min-w-0">
                                <p className="truncate text-sm font-medium flex-1">{selectedFile.name}</p>
                                <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium uppercase shrink-0">
                                    {selectedFile.source === "lrc" ? (<><FileText className="h-3 w-3"/> LRC</>) : (<><Music className="h-3 w-3"/> Embedded</>)}
                                </span>
                                <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium uppercase shrink-0">
                                    <Clock className="h-3 w-3"/>
                                    {selectedFile.synced ? "Synced" : "Plain"}
                                </span>
                            </div>
                            <div className="flex items-center gap-2">
                                {selectedFile.source === "embedded" && (<Button variant="outline" size="sm" onClick={handleExtractSelected} disabled={extracting}>
                                    {extracting ? <Spinner className="h-4 w-4"/> : <Download className="h-4 w-4"/>}
                                    Extract LRC
                                </Button>)}
                            </div>
                        </div>
                        <div className="flex-1 overflow-y-auto pt-3 min-h-0">
                            <pre className="whitespace-pre-wrap break-words font-sans text-sm leading-relaxed text-foreground/90">{selectedFile.lyrics}</pre>
                        </div>
                    </>)}
                </div>
            </div>)}
        </div>
    </div>);
}
