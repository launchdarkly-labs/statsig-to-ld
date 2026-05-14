/**
 * Statsig React Implementation Test File
 * 
 * This file demonstrates ALL Statsig React SDK features for testing the migration skill:
 * - StatsigProvider with plugins
 * - React hooks (useGateValue, useConfig, useExperiment, etc.)
 * - Session Replay and Autocapture in React
 * - Complex component patterns
 * - Loading states and error handling
 */

import React, { useState, useEffect, Suspense } from 'react';
import { createRoot } from 'react-dom/client';
import { 
  StatsigProvider,
  useGateValue,
  useConfig,
  useExperiment,
  useLayer,
  useStatsigClient,
  useStatsigUser,
  StatsigSynchronousProvider,
  useFeatureGate
} from '@statsig/react-bindings';
import { StatsigSessionReplayPlugin } from '@statsig/session-replay';
import { StatsigAutoCapturePlugin } from '@statsig/web-analytics';

// ============================================
// 1. USER CONTEXT
// ============================================
const statsigUser = {
  userID: 'react-user-456',
  email: 'jane.smith@example.com',
  custom: {
    accountType: 'business',
    company: 'TechCorp',
    role: 'manager',
    department: 'engineering',
    teamSize: 25,
    subscriptionLevel: 'professional',
    onboardingCompleted: true,
    preferredLanguage: 'en',
    timezone: 'America/New_York'
  },
  customIDs: {
    organizationID: 'org-react-789',
    tenantID: 'tenant-321',
    sessionID: 'session-654'
  },
  privateAttributes: {
    socialSecurityNumber: '***-**-1234',
    salary: 150000,
    performanceRating: 4.5
  }
};

// ============================================
// 2. MAIN APP COMPONENT WITH PROVIDER
// ============================================
function App() {
  return (
    <StatsigProvider
      sdkKey="client-react-sdk-key"
      user={statsigUser}
      options={{
        environment: { tier: 'production' },
        plugins: [
          new StatsigSessionReplayPlugin({
            maxSessionDurationMs: 1800000, // 30 minutes
            recordConsoleErrors: true,
            privacyMask: true
          }),
          new StatsigAutoCapturePlugin({
            eventFilterFunc: (event) => {
              // Filter out certain events
              return !event.eventName.includes('debug');
            }
          })
        ]
      }}
      loadingComponent={<LoadingSpinner />}
    >
      <Suspense fallback={<LoadingSpinner />}>
        <MainApplication />
      </Suspense>
    </StatsigProvider>
  );
}

// ============================================
// 3. LOADING COMPONENT
// ============================================
function LoadingSpinner() {
  return (
    <div className="loading-container">
      <div className="spinner">Loading Statsig...</div>
    </div>
  );
}

// ============================================
// 4. MAIN APPLICATION WITH ALL FEATURES
// ============================================
function MainApplication() {
  const { client } = useStatsigClient();
  const user = useStatsigUser();

  // Log custom event on mount
  useEffect(() => {
    client.logEvent('app_loaded', null, {
      component: 'MainApplication',
      user_type: user.custom?.accountType
    });
  }, [client, user]);

  return (
    <div className="app">
      <NavigationComponent />
      <DashboardComponent />
      <FeatureGatesComponent />
      <DynamicConfigComponent />
      <ExperimentComponent />
      <SettingsComponent />
      <FooterComponent />
    </div>
  );
}

// ============================================
// 5. NAVIGATION COMPONENT WITH FEATURE GATES
// ============================================
function NavigationComponent() {
  // Multiple feature gates with different hooks
  const showNewNav = useGateValue('new_navigation_menu');
  const showAdminPanel = useFeatureGate('admin_panel_access').value;
  const betaFeaturesEnabled = useGateValue('beta_features_enabled');
  const darkModeAvailable = useGateValue('dark_mode_toggle');

  const { client } = useStatsigClient();

  const handleNavClick = (item) => {
    client.logEvent('nav_item_clicked', null, {
      item_name: item,
      has_beta_access: betaFeaturesEnabled
    });
  };

  return (
    <nav className="navigation">
      <ul>
        <li onClick={() => handleNavClick('home')}>Home</li>
        <li onClick={() => handleNavClick('dashboard')}>Dashboard</li>
        
        {showNewNav && (
          <li onClick={() => handleNavClick('analytics')}>Analytics (New)</li>
        )}
        
        {showAdminPanel && (
          <li onClick={() => handleNavClick('admin')}>Admin Panel</li>
        )}
        
        {betaFeaturesEnabled && (
          <li onClick={() => handleNavClick('beta')}>Beta Features</li>
        )}
        
        {darkModeAvailable && (
          <li onClick={() => handleNavClick('theme')}>
            <DarkModeToggle />
          </li>
        )}
      </ul>
    </nav>
  );
}

// ============================================
// 6. DASHBOARD WITH DYNAMIC CONFIGS
// ============================================
function DashboardComponent() {
  const dashboardConfig = useConfig('dashboard_settings');
  const analyticsConfig = useConfig('analytics_configuration');
  const widgetConfig = useConfig('widget_layout');

  // Extract config values
  const refreshInterval = dashboardConfig.get('refresh_interval_seconds', 30);
  const maxWidgets = dashboardConfig.get('max_widgets', 6);
  const defaultView = dashboardConfig.get('default_view', 'grid');
  const showTutorial = dashboardConfig.get('show_tutorial', true);

  // Analytics config
  const chartType = analyticsConfig.get('default_chart_type', 'line');
  const dateRange = analyticsConfig.get('default_date_range', 'last_7_days');
  const metrics = analyticsConfig.get('visible_metrics', ['views', 'clicks']);

  // Widget configuration
  const widgetOrder = widgetConfig.get('widget_order', ['stats', 'chart', 'table']);
  const widgetSizes = widgetConfig.get('widget_sizes', { stats: 'small', chart: 'large' });

  const [data, setData] = useState(null);

  useEffect(() => {
    const interval = setInterval(() => {
      // Simulate data refresh
      fetchDashboardData();
    }, refreshInterval * 1000);

    return () => clearInterval(interval);
  }, [refreshInterval]);

  const fetchDashboardData = async () => {
    // Simulate API call
    const response = await fetch('/api/dashboard');
    setData(await response.json());
  };

  return (
    <div className={`dashboard view-${defaultView}`}>
      {showTutorial && <TutorialOverlay />}
      
      <div className="widgets">
        {widgetOrder.slice(0, maxWidgets).map(widgetType => (
          <Widget
            key={widgetType}
            type={widgetType}
            size={widgetSizes[widgetType]}
            chartType={chartType}
            metrics={metrics}
          />
        ))}
      </div>
      
      <DateRangePicker defaultRange={dateRange} />
    </div>
  );
}

// ============================================
// 7. EXPERIMENTS COMPONENT
// ============================================
function ExperimentComponent() {
  // Multiple experiments
  const checkoutExperiment = useExperiment('checkout_flow_v2');
  const searchExperiment = useExperiment('search_algorithm_improvement');
  const pricingExperiment = useExperiment('pricing_page_redesign');
  const onboardingExperiment = useExperiment('onboarding_flow_optimization');

  // Layer experiments
  const uiLayer = useLayer('ui_experiments_layer');
  const performanceLayer = useLayer('performance_optimizations_layer');

  // Checkout experiment parameters
  const checkoutSteps = checkoutExperiment.get('num_steps', 3);
  const showProgressBar = checkoutExperiment.get('show_progress', true);
  const autoSaveEnabled = checkoutExperiment.get('auto_save', false);

  // Search experiment parameters
  const searchAlgorithm = searchExperiment.get('algorithm', 'default');
  const instantSearchEnabled = searchExperiment.get('instant_search', false);
  const searchSuggestions = searchExperiment.get('show_suggestions', true);

  // Pricing experiment parameters
  const pricingLayout = pricingExperiment.get('layout', 'cards');
  const showTestimonials = pricingExperiment.get('show_testimonials', false);
  const highlightedPlan = pricingExperiment.get('highlighted_plan', 'professional');

  // Layer parameters
  const buttonStyle = uiLayer.get('button_style', 'rounded');
  const animationsEnabled = uiLayer.get('animations_enabled', true);
  const lazyLoadingEnabled = performanceLayer.get('lazy_loading', true);

  const { client } = useStatsigClient();

  const trackExperimentInteraction = (experimentName, action) => {
    client.logEvent('experiment_interaction', null, {
      experiment: experimentName,
      action: action,
      timestamp: Date.now()
    });
  };

  return (
    <div className="experiments-container">
      <CheckoutFlow
        steps={checkoutSteps}
        showProgress={showProgressBar}
        autoSave={autoSaveEnabled}
        onInteraction={(action) => trackExperimentInteraction('checkout_flow_v2', action)}
      />
      
      <SearchBar
        algorithm={searchAlgorithm}
        instantSearch={instantSearchEnabled}
        suggestions={searchSuggestions}
        onSearch={(query) => trackExperimentInteraction('search_algorithm', 'search')}
      />
      
      <PricingSection
        layout={pricingLayout}
        testimonials={showTestimonials}
        highlighted={highlightedPlan}
        buttonStyle={buttonStyle}
        animated={animationsEnabled}
      />
      
      {lazyLoadingEnabled && <LazyLoadedContent />}
    </div>
  );
}

// ============================================
// 8. FEATURE GATES COMPONENT
// ============================================
function FeatureGatesComponent() {
  // Various gate checking patterns
  const premiumFeatures = useGateValue('premium_features_enabled');
  const exportEnabled = useGateValue('data_export_enabled');
  const apiV2Enabled = useGateValue('api_v2_enabled');
  const bulkOperations = useGateValue('bulk_operations_enabled');
  const advancedFilters = useGateValue('advanced_filters_enabled');
  const customReports = useGateValue('custom_reports_enabled');
  
  // Gates with special characters
  const betaFeature = useGateValue('beta-feature-flag');
  const alphaFeature = useGateValue('alpha_testing_enabled');
  
  const { client } = useStatsigClient();

  // Manual gate exposure
  const checkSilentGate = () => {
    const value = client.checkGate('silent_gate', { disableExposureLog: true });
    // Later expose manually
    if (value) {
      client.manuallyLogGateExposure('silent_gate');
    }
    return value;
  };

  return (
    <div className="features">
      {premiumFeatures && <PremiumFeatureSet />}
      {exportEnabled && <ExportButton />}
      {apiV2Enabled && <APIV2Indicator />}
      {bulkOperations && <BulkActionToolbar />}
      {advancedFilters && <AdvancedFilterPanel />}
      {customReports && <CustomReportBuilder />}
      {betaFeature && <BetaFeatureBadge />}
      {alphaFeature && <AlphaTestingPanel />}
      {checkSilentGate() && <SilentFeature />}
    </div>
  );
}

// ============================================
// 9. SETTINGS COMPONENT WITH USER UPDATES
// ============================================
function SettingsComponent() {
  const { client } = useStatsigClient();
  const user = useStatsigUser();
  const [settings, setSettings] = useState({});

  const updateUserProfile = async (newSettings) => {
    // Update local state
    setSettings(newSettings);

    // Update Statsig user
    await client.updateUserAsync({
      ...user,
      custom: {
        ...user.custom,
        ...newSettings,
        lastUpdated: new Date().toISOString()
      }
    });

    // Log the update
    client.logEvent('user_settings_updated', null, {
      changed_fields: Object.keys(newSettings),
      timestamp: Date.now()
    });
  };

  return (
    <div className="settings">
      <h2>User Settings</h2>
      <form onSubmit={(e) => {
        e.preventDefault();
        updateUserProfile(settings);
      }}>
        <input
          type="text"
          placeholder="Company"
          onChange={(e) => setSettings({ ...settings, company: e.target.value })}
        />
        <select onChange={(e) => setSettings({ ...settings, role: e.target.value })}>
          <option value="admin">Admin</option>
          <option value="manager">Manager</option>
          <option value="user">User</option>
        </select>
        <button type="submit">Update Profile</button>
      </form>
    </div>
  );
}

// ============================================
// 10. HELPER COMPONENTS
// ============================================
function DarkModeToggle() {
  const [isDark, setIsDark] = useState(false);
  const { client } = useStatsigClient();

  const toggleDarkMode = () => {
    setIsDark(!isDark);
    client.logEvent('dark_mode_toggled', null, { enabled: !isDark });
  };

  return (
    <button onClick={toggleDarkMode}>
      {isDark ? '🌙' : '☀️'}
    </button>
  );
}

function TutorialOverlay() {
  return <div className="tutorial">Welcome! Here's how to use the dashboard...</div>;
}

function Widget({ type, size, chartType, metrics }) {
  return (
    <div className={`widget widget-${type} size-${size}`}>
      <h3>{type}</h3>
      {type === 'chart' && <Chart type={chartType} metrics={metrics} />}
    </div>
  );
}

function Chart({ type, metrics }) {
  return <div>Chart: {type} showing {metrics.join(', ')}</div>;
}

function DateRangePicker({ defaultRange }) {
  return <div>Date Range: {defaultRange}</div>;
}

function CheckoutFlow({ steps, showProgress, autoSave, onInteraction }) {
  return (
    <div onClick={() => onInteraction('checkout_clicked')}>
      Checkout: {steps} steps {showProgress && 'with progress'}
    </div>
  );
}

function SearchBar({ algorithm, instantSearch, suggestions, onSearch }) {
  return (
    <input 
      type="search" 
      placeholder={`Search (${algorithm})`}
      onChange={(e) => instantSearch && onSearch(e.target.value)}
    />
  );
}

function PricingSection({ layout, testimonials, highlighted, buttonStyle, animated }) {
  return (
    <div className={`pricing layout-${layout} ${animated ? 'animated' : ''}`}>
      Pricing with {highlighted} highlighted
    </div>
  );
}

function LazyLoadedContent() {
  return <div>Lazy loaded content</div>;
}

function PremiumFeatureSet() {
  return <div>Premium Features Enabled</div>;
}

function ExportButton() {
  const { client } = useStatsigClient();
  
  const handleExport = () => {
    client.logEvent('data_exported', null, {
      format: 'csv',
      timestamp: Date.now()
    });
  };

  return <button onClick={handleExport}>Export Data</button>;
}

function APIV2Indicator() {
  return <span className="badge">API v2</span>;
}

function BulkActionToolbar() {
  return <div className="toolbar">Bulk Actions</div>;
}

function AdvancedFilterPanel() {
  return <div className="filters">Advanced Filters</div>;
}

function CustomReportBuilder() {
  return <div className="reports">Custom Reports</div>;
}

function BetaFeatureBadge() {
  return <span className="beta">BETA</span>;
}

function AlphaTestingPanel() {
  return <div className="alpha">Alpha Testing Features</div>;
}

function SilentFeature() {
  return <div>Silent Feature (manually exposed)</div>;
}

function FooterComponent() {
  const { client } = useStatsigClient();

  useEffect(() => {
    // Log page unload
    const handleUnload = () => {
      client.logEvent('page_unload', null, {
        session_duration: Date.now() - window.sessionStart
      });
      client.shutdown();
    };

    window.addEventListener('beforeunload', handleUnload);
    return () => window.removeEventListener('beforeunload', handleUnload);
  }, [client]);

  return <footer>© 2024 Test App</footer>;
}

// ============================================
// 11. ALTERNATIVE PROVIDER PATTERN
// ============================================
function AlternativeApp() {
  // Synchronous provider for SSR or pre-initialized scenarios
  const initializeValues = {
    feature_gates: {
      new_feature: true,
      beta_access: false
    },
    dynamic_configs: {
      homepage_config: {
        title: 'Welcome'
      }
    },
    layer_configs: {},
    user: statsigUser
  };

  return (
    <StatsigSynchronousProvider
      sdkKey="client-sync-sdk-key"
      initializeValues={initializeValues}
    >
      <MainApplication />
    </StatsigSynchronousProvider>
  );
}

// ============================================
// 12. RENDER THE APP
// ============================================
window.sessionStart = Date.now();

const container = document.getElementById('root');
const root = createRoot(container);
root.render(<App />);