// NOTE: This component is not currently used in the app but preserved for potential future marketing/demo purposes
// It provides an interactive mockup of the Omnara Command Center interface

import { useState } from "react";
import { CheckCircle, Clock, Activity, ArrowRight, FileCode, Terminal, MessageCircle, User, Bell, Smartphone, Send } from "lucide-react";
import { useMobile } from '../hooks/use-mobile';
import ClaudeIcon from './claude-color.svg';

const InteractiveProductMockup = () => {
  const isMobile = useMobile();
  const [showResponse, setShowResponse] = useState(false);
  const [showGitDiffs, setShowGitDiffs] = useState(false);

  const conversation = [
    { 
      id: 1, 
      type: 'step',
      text: "Analyzing the provided screenshots to understand the desired chat interface design", 
      completed: true,
      timestamp: "33 minutes ago"
    },
    { 
      id: 2, 
      type: 'step',
      text: "Updating InteractiveProductMockup to reposition Claude logo and update message styling", 
      completed: true,
      timestamp: "33 minutes ago"
    },
    { 
      id: 3, 
      type: 'step',
      text: "Fixing git diff viewer text visibility in the InteractiveProductMockup component", 
      completed: true,
      timestamp: "26 minutes ago"
    },
    { 
      id: 4, 
      type: 'question',
      text: "I've noticed the git diff indentation doesn't match standard code formatting. Should I fix the indentation to make it more readable?", 
      completed: true,
      timestamp: "26 minutes ago"
    },
    {
      id: 5,
      type: 'user_response',
      text: "Yes, fix the indentation here",
      timestamp: "15 minutes ago"
    },
    { 
      id: 6, 
      type: 'step',
      text: "Fixing git diff indentation in the InteractiveProductMockup component", 
      completed: true,
      timestamp: "15 minutes ago"
    },
    { 
      id: 7, 
      type: 'step',
      text: "Looking for Claude Code SVG in the public folder to replace user icon", 
      completed: true,
      timestamp: "10 minutes ago"
    },
    { 
      id: 8, 
      type: 'step',
      text: "Updating Claude icon to use claude-color.svg instead of claude.svg", 
      completed: false,
      current: true,
      timestamp: "just now"
    },
    { 
      id: 9, 
      type: 'question',
      text: "Should I also update the Header component to use the same Claude icon for consistency across the app?", 
      completed: false,
      current: true,
      timestamp: "just now"
    }
  ];

  const currentQuestion = conversation.find(item => item.type === 'question' && item.current);
  const questionText = currentQuestion?.text || "";
  const options = ["Yes, update it everywhere", "No, keep it as is"];

  const handleRespond = () => {
    setShowResponse(true);
    setTimeout(() => {
      setShowResponse(false);
    }, 2000);
  };

  return (
    <div className="w-full max-w-6xl mx-auto">
      <div className="relative">
        {/* Glow effect */}
        <div className="absolute -inset-4 bg-gradient-to-r from-electric-accent/20 via-electric-violet/15 to-electric-violet/10 rounded-2xl blur-2xl opacity-75"></div>
        
        {/* Main container */}
        <div className="relative bg-charcoal/95 rounded-2xl border-2 border-electric-violet/40 overflow-hidden backdrop-blur-sm shadow-[0_20px_40px_rgba(0,0,0,0.3)]">
          
          {/* Header */}
          <div className="bg-midnight-blue/50 border-b border-white/10 px-4 sm:px-6 py-4">
            <div className="flex items-center justify-between">
              <div className="flex items-center space-x-3">
                <div className="flex space-x-2">
                  <div className="w-3 h-3 bg-red-400 rounded-full"></div>
                  <div className="w-3 h-3 bg-yellow-400 rounded-full"></div>
                  <div className="w-3 h-3 bg-green-400 rounded-full"></div>
                </div>
                <h3 className="text-white font-semibold text-base sm:text-lg">Omnara Command Center</h3>
              </div>
              <div className="flex items-center space-x-2">
                <div className="w-2 h-2 bg-green-400 rounded-full animate-pulse"></div>
                <span className="text-green-400 text-xs sm:text-sm">Live</span>
              </div>
            </div>
          </div>

          {/* Quick Actions Bar */}
          <div className="bg-charcoal/50 border-b border-white/10 px-4 sm:px-6 py-3">
            <div className="flex items-center justify-between flex-wrap gap-2">
              <div className="flex items-center space-x-2 sm:space-x-4">
                <button className="flex items-center space-x-1 sm:space-x-2 px-2 sm:px-3 py-1 sm:py-1.5 bg-midnight-blue/40 text-off-white/80 rounded-lg hover:bg-midnight-blue/60 hover:text-white transition-all text-xs sm:text-sm border border-white/10">
                  <svg className="w-3 h-3 sm:w-4 sm:h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 6V4m0 2a2 2 0 100 4m0-4a2 2 0 110 4m-6 8a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4m6 6v10m6-2a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4" />
                  </svg>
                  <span className="hidden sm:inline">Filters</span>
                </button>
                <button className="flex items-center space-x-1 sm:space-x-2 px-2 sm:px-3 py-1 sm:py-1.5 bg-midnight-blue/40 text-off-white/80 rounded-lg hover:bg-midnight-blue/60 hover:text-white transition-all text-xs sm:text-sm border border-white/10">
                  <svg className="w-3 h-3 sm:w-4 sm:h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                  </svg>
                  <span className="hidden sm:inline">Search logs</span>
                </button>
                <div className="flex items-center space-x-1 sm:space-x-2 text-xs text-off-white/50">
                  <div className="w-2 h-2 bg-green-400 rounded-full animate-pulse"></div>
                  <span className="hidden sm:inline">Auto-refresh: 5s</span>
                  <span className="sm:hidden">5s</span>
                </div>
              </div>
              <div className="flex items-center space-x-2">
                <button className="p-1.5 text-off-white/60 hover:text-white hover:bg-white/10 rounded transition-colors" title="Minimize sidebar">
                  <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M11 19l-7-7 7-7m8 14l-7-7 7-7" />
                  </svg>
                </button>
                <button className="p-1.5 text-off-white/60 hover:text-white hover:bg-white/10 rounded transition-colors" title="Settings">
                  <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                  </svg>
                </button>
              </div>
            </div>
          </div>

          {/* Enhanced Agent Header */}
          <div className="bg-midnight-blue/30 border-b border-white/10 px-4 sm:px-6 py-4">
            <div className="flex items-center justify-between mb-3">
              <div className="flex items-center space-x-3 sm:space-x-4">
                <div className="w-12 h-12 sm:w-14 sm:h-14 bg-gradient-to-br from-electric-accent/50 to-electric-violet/50 rounded-full flex items-center justify-center border-2 border-electric-accent/30">
                  <span className="text-white text-base sm:text-lg font-bold">AI</span>
                </div>
                <div className="flex-1 min-w-0">
                  <h3 className="text-white text-lg sm:text-xl font-semibold">Claude Code</h3>
                  <div className="text-off-white/60 text-xs sm:text-sm truncate">Instance 9a8633dd • <span className="text-yellow-400 font-medium">Waiting for input</span> • 14m 32s</div>
                </div>
              </div>
              <div className="flex items-center space-x-2">
                {/* Notification bell */}
                <button className="relative p-1.5 sm:p-2 text-off-white/60 hover:text-white hover:bg-white/10 rounded-lg transition-colors" title="View notifications">
                  <Bell className="w-4 h-4 sm:w-5 sm:h-5" />
                  <div className="absolute top-0 right-0 w-2 h-2 bg-red-500 rounded-full"></div>
                </button>
                <button className="p-1.5 sm:p-2 text-off-white/60 hover:text-white hover:bg-white/10 rounded-lg transition-colors" title="Pause session">
                  <svg className="w-4 h-4 sm:w-5 sm:h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10 9v6m4-6v6m7-3a9 9 0 11-18 0 9 9 0 0118 0z" />
                  </svg>
                </button>
                <button className="p-1.5 sm:p-2 text-off-white/60 hover:text-white hover:bg-white/10 rounded-lg transition-colors hidden sm:block" title="Export logs">
                  <svg className="w-4 h-4 sm:w-5 sm:h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
                  </svg>
                </button>
                <button className="px-3 py-1.5 sm:px-4 sm:py-2 bg-electric-accent text-midnight-blue text-xs sm:text-sm font-medium hover:bg-electric-accent/90 rounded-lg transition-colors">
                  Mark Complete
                </button>
              </div>
            </div>
          </div>

          {/* Content - Simple Chat View */}
          <div className="p-4 sm:p-6" style={{ minHeight: isMobile ? '400px' : '500px' }}>
            {showResponse ? (
              <div className="flex items-center justify-center h-64">
                <div className="text-center">
                  <CheckCircle className="w-16 h-16 text-green-400 mx-auto mb-4 animate-scaleIn" />
                  <p className="text-white text-lg font-medium">Response sent successfully!</p>
                  <p className="text-off-white/60 text-sm mt-2">Returning to monitoring...</p>
                </div>
              </div>
            ) : (
              <div className="space-y-6">
                {/* View Options and First Step - Positioned in top bar */}
                <div className="flex flex-col sm:flex-row sm:justify-between sm:items-center gap-4 mb-6">
                  {/* First step */}
                  {conversation.length > 0 && (
                    <div className="flex items-center space-x-2 sm:space-x-3">
                      <div className="flex items-center space-x-1 sm:space-x-2">
                        <div className="w-5 h-5 sm:w-6 sm:h-6 rounded-full bg-midnight-blue/60 flex items-center justify-center flex-shrink-0">
                          <img 
                            src={ClaudeIcon} 
                            alt="Claude" 
                            className="w-3 h-3 sm:w-4 sm:h-4"
                          />
                        </div>
                        <CheckCircle className="w-4 h-4 sm:w-5 sm:h-5 text-green-400" />
                      </div>
                      <div className="flex-1 min-w-0">
                        <p className="text-xs text-off-white/60 font-medium">claude-code</p>
                        <p className="text-sm sm:text-base text-white break-words">{conversation[0].text}</p>
                      </div>
                    </div>
                  )}
                  
                  {/* Buttons */}
                  <div className="flex items-center space-x-2 self-end sm:self-auto">
                    <button 
                      onClick={() => setShowGitDiffs(!showGitDiffs)}
                      className={`flex items-center space-x-1 px-2 sm:px-3 py-1 sm:py-1.5 rounded-lg text-xs sm:text-sm transition-all ${
                        showGitDiffs 
                          ? 'bg-electric-accent/20 text-electric-accent border border-electric-accent/30' 
                          : 'bg-midnight-blue/40 text-off-white/60 hover:text-white border border-white/10'
                      }`}
                    >
                      <FileCode className="w-3 h-3 sm:w-4 sm:h-4" />
                      <span>Git Diffs</span>
                    </button>
                    <button 
                      className="flex items-center space-x-1 px-2 sm:px-3 py-1 sm:py-1.5 rounded-lg text-xs sm:text-sm bg-midnight-blue/40 text-off-white/60 hover:text-white border border-white/10 transition-all"
                    >
                      <Terminal className="w-3 h-3 sm:w-4 sm:h-4" />
                      <span>Raw Logs</span>
                    </button>
                  </div>
                </div>

                {/* Main Content */}
                {!showGitDiffs ? (
                  <>
                    {/* Conversation History */}
                    <div className="space-y-6">
                      {conversation.slice(1).map((item, index) => {
                        const actualIndex = index + 1;
                        const isCurrentQuestion = item.type === 'question' && item.current;
                        const isFirstAIMessage = actualIndex === 0 && (item.type === 'step' || item.type === 'question');
                        
                        return (
                          <div key={item.id} className="flex items-start space-x-2 sm:space-x-3 relative">
                            {/* Icons column */}
                            <div className="flex items-start space-x-1 sm:space-x-2">
                              {/* Claude logo - always shown for AI messages */}
                              {(item.type === 'step' || item.type === 'question') && (
                                <div className="w-5 h-5 sm:w-6 sm:h-6 rounded-full bg-midnight-blue/60 flex items-center justify-center flex-shrink-0">
                                  <img 
                                    src={ClaudeIcon} 
                                    alt="Claude" 
                                    className="w-3 h-3 sm:w-4 sm:h-4"
                                  />
                                </div>
                              )}
                              
                              {/* Status indicator */}
                              <div className="mt-0.5 flex-shrink-0">
                                {item.type === 'step' ? (
                                  item.completed ? (
                                    <CheckCircle className="w-4 h-4 sm:w-5 sm:h-5 text-green-400" />
                                  ) : item.current ? (
                                    <div className="w-4 h-4 sm:w-5 sm:h-5 border-2 border-electric-accent rounded-full flex items-center justify-center">
                                      <div className="w-2 h-2 sm:w-2.5 sm:h-2.5 bg-electric-accent rounded-full animate-pulse"></div>
                                    </div>
                                  ) : (
                                    <div className="w-4 h-4 sm:w-5 sm:h-5 border-2 border-white/20 rounded-full"></div>
                                  )
                                ) : item.type === 'question' ? (
                                  isCurrentQuestion ? (
                                    <div className="w-4 h-4 sm:w-5 sm:h-5 bg-yellow-500/20 border-2 border-yellow-500 rounded-full flex items-center justify-center animate-pulse">
                                      <MessageCircle className="w-2.5 h-2.5 sm:w-3 sm:h-3 text-yellow-500" />
                                    </div>
                                  ) : (
                                    <CheckCircle className="w-4 h-4 sm:w-5 sm:h-5 text-green-400" />
                                  )
                                ) : item.type === 'user_response' ? (
                                  <User className="w-4 h-4 sm:w-5 sm:h-5 text-off-white/60" />
                                ) : (
                                  <div className="w-4 h-4 sm:w-5 sm:h-5 border-2 border-white/20 rounded-full"></div>
                                )}
                              </div>
                            </div>
                            <div className="flex-1 min-w-0">
                              <div className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-1">
                                <div className="flex-1 min-w-0">
                                  {/* Agent name for AI messages - only show on first message */}
                                  {isFirstAIMessage && (
                                    <p className="text-xs text-off-white/60 font-medium mb-1">claude-code</p>
                                  )}
                                  {item.type === 'user_response' && (
                                    <p className="text-xs text-off-white/60 font-medium mb-1">You</p>
                                  )}
                                  <p className={`text-sm sm:text-base break-words ${
                                    item.type === 'step' ? (item.completed ? 'text-white' : 'text-off-white/40') :
                                    item.type === 'question' ? (isCurrentQuestion ? 'text-white font-medium' : 'text-white') :
                                    'text-white/90'
                                  }`}>
                                    {item.text}
                                  </p>
                                  {isCurrentQuestion && (
                                    <div className="mt-2 flex flex-col sm:flex-row sm:items-center gap-2 sm:gap-4 text-xs">
                                      <span className="flex items-center space-x-1 text-yellow-500">
                                        <Clock className="w-3 h-3" />
                                        <span>Just now</span>
                                      </span>
                                      <span className="flex items-center space-x-1 text-yellow-500/80">
                                        <Activity className="w-3 h-3 animate-pulse" />
                                        <span>Waiting for your input...</span>
                                      </span>
                                    </div>
                                  )}
                                </div>
                                <span className="text-xs text-off-white/40 whitespace-nowrap">{item.timestamp}</span>
                              </div>
                            </div>
                          </div>
                        );
                      })}
                    </div>

                    {/* Question */}
                    <div className="bg-gradient-to-r from-electric-violet/10 to-electric-accent/10 rounded-xl p-4 sm:p-6 border border-white/10 mt-6">
                      <p className="text-white text-base sm:text-lg font-medium mb-4 sm:mb-6">{questionText}</p>
                      
                      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 sm:gap-3 mb-4 sm:mb-6">
                        {options.map((option, index) => (
                          <button 
                            key={index}
                            className="px-4 sm:px-6 py-2 sm:py-3 bg-electric-accent/20 rounded-lg text-sm sm:text-base text-electric-accent border border-electric-accent/30 hover:bg-electric-accent/30 hover:border-electric-accent/50 transition-all duration-200"
                          >
                            {option}
                          </button>
                        ))}
                      </div>

                      <div className="flex flex-col sm:flex-row sm:items-center gap-3">
                        <input 
                          type="text" 
                          placeholder="Type your response here..."
                          className="flex-1 bg-midnight-blue/30 border border-white/10 rounded-lg px-3 sm:px-4 py-2 sm:py-3 text-white text-sm sm:text-base placeholder-off-white/40 focus:outline-none focus:border-electric-accent/50 focus:bg-midnight-blue/50 transition-all"
                        />
                        <button 
                          onClick={handleRespond}
                          className="px-4 sm:px-6 py-2 sm:py-3 bg-electric-accent text-midnight-blue rounded-lg text-sm sm:text-base font-semibold hover:bg-electric-accent/90 transition-all duration-200 shadow-lg hover:shadow-electric-accent/25"
                        >
                          Send
                        </button>
                      </div>
                    </div>
                  </>
                ) : (
                  /* Git Diffs View */
                  <div className="space-y-4">
                    <div className="bg-charcoal/50 rounded-lg border border-white/10 overflow-hidden">
                      <div className="bg-midnight-blue/50 px-4 py-2 border-b border-white/10">
                        <div className="flex items-center justify-between">
                          <span className="text-sm font-mono text-white">src/components/InteractiveProductMockup.tsx</span>
                          <span className="text-xs text-off-white/60">+5 -3</span>
                        </div>
                      </div>
                      <div className="p-3 sm:p-4 font-mono text-xs sm:text-sm overflow-x-auto">
                        <div className="text-cyan-400">@@ -236,12 +236,12 @@ export default function InteractiveProductMockup() {'{'}</div>
                        <pre className="text-off-white/80">  {'('}item.type === 'step' || item.type === 'question'{')'} && {'('}</pre>
                        <pre className="text-off-white/80">    &lt;div className="w-6 h-6 rounded-full bg-midnight-blue/60 flex items-center justify-center flex-shrink-0"&gt;</pre>
                        <pre className="bg-red-500/10 border-l-2 border-red-500 pl-2 text-red-400">-     &lt;svg width="16" height="16" viewBox="0 0 24 24" fill="none" className="text-electric-accent"&gt;</pre>
                        <pre className="bg-red-500/10 border-l-2 border-red-500 pl-2 text-red-400">-       &lt;path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10..." fill="currentColor"/&gt;</pre>
                        <pre className="bg-red-500/10 border-l-2 border-red-500 pl-2 text-red-400">-     &lt;/svg&gt;</pre>
                        <pre className="bg-green-500/10 border-l-2 border-green-500 pl-2 text-green-400">+     &lt;img </pre>
                        <pre className="bg-green-500/10 border-l-2 border-green-500 pl-2 text-green-400">+       src={'{'}ClaudeIcon{'}'} </pre>
                        <pre className="bg-green-500/10 border-l-2 border-green-500 pl-2 text-green-400">+       alt="Claude" </pre>
                        <pre className="bg-green-500/10 border-l-2 border-green-500 pl-2 text-green-400">+       className="w-4 h-4"</pre>
                        <pre className="bg-green-500/10 border-l-2 border-green-500 pl-2 text-green-400">+     /&gt;</pre>
                        <pre className="text-off-white/80">    &lt;/div&gt;</pre>
                        <pre className="text-off-white/80">  {')'}{'}'}</pre>
                      </div>
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Feature callouts - Commented out for now */}
      {/* <div className="mt-6 sm:mt-8 grid grid-cols-1 sm:grid-cols-3 gap-4 text-center">
        <div className="text-off-white/60 text-xs sm:text-sm">
          <div className="text-electric-accent font-semibold mb-1">Full Execution Trace</div>
          Debug every step, tool call, and decision
        </div>
        <div className="text-off-white/60 text-xs sm:text-sm">
          <div className="text-electric-accent font-semibold mb-1">Interactive Debugging</div>
          Step through agent execution in real-time
        </div>
        <div className="text-off-white/60 text-xs sm:text-sm">
          <div className="text-electric-accent font-semibold mb-1">Deploy with Confidence</div>
          From prototype to production-ready
        </div>
      </div> */}

      {/* Mobile-focused messaging */}
      <div className="mt-8 text-center">
        <div className="relative">
          {/* Gradient background */}
          <div className="absolute inset-0 bg-gradient-to-r from-electric-violet/10 via-electric-accent/10 to-electric-violet/10 rounded-2xl blur-xl"></div>
          
          <div className="relative space-y-4 sm:space-y-6 py-6 sm:py-8 px-4">
            <div className="flex flex-col sm:flex-row items-center justify-center gap-4 sm:gap-8 lg:gap-12">
              <div className="flex flex-col items-center space-y-2 flex-1 sm:flex-none">
                <div className="w-10 h-10 sm:w-12 sm:h-12 bg-electric-accent/20 rounded-full flex items-center justify-center">
                  <Smartphone className="w-5 h-5 sm:w-6 sm:h-6 text-electric-accent" />
                </div>
                <p className="text-white font-semibold text-xs sm:text-sm lg:text-base text-center px-2">Launch agents from your phone</p>
              </div>
              
              <div className="hidden sm:block w-8 lg:w-12 h-[2px] bg-gradient-to-r from-electric-accent/30 to-electric-violet/30"></div>
              
              <div className="flex flex-col items-center space-y-2 flex-1 sm:flex-none">
                <div className="w-10 h-10 sm:w-12 sm:h-12 bg-electric-violet/20 rounded-full flex items-center justify-center">
                  <Bell className="w-5 h-5 sm:w-6 sm:h-6 text-electric-violet" />
                </div>
                <p className="text-white font-semibold text-xs sm:text-sm lg:text-base text-center px-2">Get notified when they need help</p>
              </div>
              
              <div className="hidden sm:block w-8 lg:w-12 h-[2px] bg-gradient-to-r from-electric-violet/30 to-electric-accent/30"></div>
              
              <div className="flex flex-col items-center space-y-2 flex-1 sm:flex-none">
                <div className="w-10 h-10 sm:w-12 sm:h-12 bg-electric-accent/20 rounded-full flex items-center justify-center">
                  <Send className="w-5 h-5 sm:w-6 sm:h-6 text-electric-accent" />
                </div>
                <p className="text-white font-semibold text-xs sm:text-sm lg:text-base text-center px-2">Send them back to work</p>
              </div>
            </div>
            
            <p className="text-off-white/60 text-xs sm:text-sm max-w-2xl mx-auto px-4">
              Stay connected with your AI agents anywhere, anytime. Get instant notifications when they need your input and guide them back on track with a simple tap.
            </p>
          </div>
        </div>
      </div>
    </div>
  );
};

export default InteractiveProductMockup;