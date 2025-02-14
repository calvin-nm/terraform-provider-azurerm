package appconfiguration

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Azure/go-autorest/autorest"
	"github.com/hashicorp/go-azure-sdk/resource-manager/appconfiguration/2022-05-01/configurationstores"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/tf"
	"github.com/hashicorp/terraform-provider-azurerm/internal/sdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/appconfiguration/migration"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/appconfiguration/parse"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/appconfiguration/sdk/1.0/appconfiguration"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/appconfiguration/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tags"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/validation"
	"github.com/hashicorp/terraform-provider-azurerm/utils"
)

const (
	FeatureKeyContentType = "application/vnd.microsoft.appconfig.ff+json;charset=utf-8"
	FeatureKeyPrefix      = ".appconfig.featureflag"
)

type FeatureResource struct{}

var _ sdk.ResourceWithUpdate = FeatureResource{}

var _ sdk.ResourceWithStateMigration = FeatureResource{}

type FeatureResourceModel struct {
	ConfigurationStoreId string                       `tfschema:"configuration_store_id"`
	Description          string                       `tfschema:"description"`
	Enabled              bool                         `tfschema:"enabled"`
	Key                  string                       `tfschema:"key"`
	Name                 string                       `tfschema:"name"`
	Label                string                       `tfschema:"label"`
	Locked               bool                         `tfschema:"locked"`
	Tags                 map[string]interface{}       `tfschema:"tags"`
	PercentageFilter     int                          `tfschema:"percentage_filter_value"`
	TimewindowFilters    []TimewindowFilterParameters `tfschema:"timewindow_filter"`
	TargetingFilters     []TargetingFilterAudience    `tfschema:"targeting_filter"`
}

func (k FeatureResource) Arguments() map[string]*pluginsdk.Schema {
	return map[string]*pluginsdk.Schema{
		"configuration_store_id": {
			Type:         pluginsdk.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: configurationstores.ValidateConfigurationStoreID,
		},
		"description": {
			Type:     pluginsdk.TypeString,
			Optional: true,
		},
		"enabled": {
			Type:     pluginsdk.TypeBool,
			Optional: true,
		},
		"key": {
			Type:         pluginsdk.TypeString,
			Optional:     true,
			Computed:     true,
			ForceNew:     true,
			ValidateFunc: validate.AppConfigurationFeatureKey,
		},
		"name": {
			Type:         pluginsdk.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: validate.AppConfigurationFeatureName,
		},
		"etag": {
			Type:     pluginsdk.TypeString,
			Computed: true,
			Optional: true,
		},
		"label": {
			Type:     pluginsdk.TypeString,
			Optional: true,
			ForceNew: true,
		},
		"locked": {
			Type:     pluginsdk.TypeBool,
			Optional: true,
			Default:  false,
		},
		"percentage_filter_value": {
			Type:         pluginsdk.TypeInt,
			Optional:     true,
			ValidateFunc: validation.IntBetween(0, 100),
		},
		"targeting_filter": {
			Type:     pluginsdk.TypeList,
			Optional: true,
			Elem: &pluginsdk.Resource{
				Schema: map[string]*schema.Schema{
					"default_rollout_percentage": {
						Type:         pluginsdk.TypeInt,
						Required:     true,
						ValidateFunc: validation.IntBetween(0, 100),
					},

					"groups": {
						Type:     pluginsdk.TypeList,
						Optional: true,
						Elem: &pluginsdk.Resource{
							Schema: map[string]*schema.Schema{
								"name": {
									Type:     pluginsdk.TypeString,
									Required: true,
								},
								"rollout_percentage": {
									Type:         pluginsdk.TypeInt,
									Required:     true,
									ValidateFunc: validation.IntBetween(0, 100),
								},
							},
						},
					},
					"users": {
						Type:     pluginsdk.TypeList,
						Optional: true,
						Elem: &schema.Schema{
							Type:         schema.TypeString,
							ValidateFunc: validation.StringIsNotEmpty,
						},
					},
				},
			},
		},
		"timewindow_filter": {
			Type:     pluginsdk.TypeList,
			Optional: true,
			Elem: &pluginsdk.Resource{
				Schema: map[string]*schema.Schema{
					"start": {
						Type:         pluginsdk.TypeString,
						Optional:     true,
						ValidateFunc: validation.IsRFC3339Time,
					},
					"end": {
						Type:         pluginsdk.TypeString,
						Optional:     true,
						ValidateFunc: validation.IsRFC3339Time,
					},
				},
			},
		},
		"tags": tags.Schema(),
	}
}

func (k FeatureResource) Attributes() map[string]*pluginsdk.Schema {
	return map[string]*pluginsdk.Schema{}
}

func (k FeatureResource) ModelObject() interface{} {
	return &FeatureResourceModel{}
}

func (k FeatureResource) ResourceType() string {
	return "azurerm_app_configuration_feature"
}

func (k FeatureResource) Create() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			var model FeatureResourceModel
			if err := metadata.Decode(&model); err != nil {
				return fmt.Errorf("decoding %+v", err)
			}

			configurationStoreId, err := configurationstores.ParseConfigurationStoreID(model.ConfigurationStoreId)
			if err != nil {
				return err
			}

			configurationStoreEndpoint, err := metadata.Client.AppConfiguration.EndpointForConfigurationStore(ctx, *configurationStoreId)
			if err != nil {
				return fmt.Errorf("retrieving Endpoint for feature %q in %q: %s", model.Name, *configurationStoreId, err)
			}

			client, err := metadata.Client.AppConfiguration.DataPlaneClientWithEndpoint(*configurationStoreEndpoint)
			if err != nil {
				return err
			}

			// users can customize the key, but if they don't we use the name
			rawKey := model.Name
			if model.Key != "" {
				rawKey = model.Key
			}
			featureKey := fmt.Sprintf("%s/%s", FeatureKeyPrefix, rawKey)

			nestedItemId, err := parse.NewNestedItemID(client.Endpoint, featureKey, model.Label)
			if err != nil {
				return err
			}

			deadline, ok := ctx.Deadline()
			if !ok {
				return fmt.Errorf("internal-error: context had no deadline")
			}

			// from https://learn.microsoft.com/en-us/azure/azure-app-configuration/concept-enable-rbac#azure-built-in-roles-for-azure-app-configuration
			// allow some time for role permission to be done propagated
			metadata.Logger.Infof("[DEBUG] Waiting for App Configuration Key %q read permission to be done propagated", featureKey)
			stateConf := &pluginsdk.StateChangeConf{
				Pending:      []string{"Forbidden"},
				Target:       []string{"Error", "Exists"},
				Refresh:      appConfigurationGetKeyRefreshFunc(ctx, client, featureKey, model.Label),
				PollInterval: 20 * time.Second,
				Timeout:      time.Until(deadline),
			}

			if _, err = stateConf.WaitForStateContext(ctx); err != nil {
				return fmt.Errorf("waiting for App Configuration Key %q read permission to be propagated: %+v", featureKey, err)
			}

			kv, err := client.GetKeyValue(ctx, featureKey, model.Label, "", "", "", []string{})
			if err != nil {
				if v, ok := err.(autorest.DetailedError); ok {
					if !utils.ResponseWasNotFound(autorest.Response{Response: v.Response}) {
						return fmt.Errorf("got http status code %d while checking for key's %q existence: %+v", v.Response.StatusCode, featureKey, v.Error())
					}
				} else {
					return fmt.Errorf("while checking for key's %q existence: %+v", featureKey, err)
				}
			} else if kv.Response.StatusCode == 200 {
				return tf.ImportAsExistsError(k.ResourceType(), nestedItemId.ID())
			}

			err = createOrUpdateFeature(ctx, client, model)
			if err != nil {
				return fmt.Errorf("while creating feature: %+v", err)
			}

			metadata.SetID(nestedItemId)
			return nil
		},
		Timeout: 45 * time.Minute,
	}
}

func (k FeatureResource) Read() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			nestedItemId, err := parse.ParseNestedItemID(metadata.ResourceData.Id())
			if err != nil {
				return fmt.Errorf("while parsing resource ID: %+v", err)
			}

			resourceClient := metadata.Client.Resource
			configurationStoreIdRaw, err := metadata.Client.AppConfiguration.ConfigurationStoreIDFromEndpoint(ctx, resourceClient, nestedItemId.ConfigurationStoreEndpoint)
			if err != nil {
				return fmt.Errorf("while retrieving the Resource ID of Configuration Store at Endpoint: %q: %s", nestedItemId.ConfigurationStoreEndpoint, err)
			}
			if configurationStoreIdRaw == nil {
				// if the AppConfiguration is gone then all the data inside it is too
				log.Printf("[DEBUG] Unable to determine the Resource ID for Configuration Store at Endpoint %q - removing from state", nestedItemId.ConfigurationStoreEndpoint)
				return metadata.MarkAsGone(nestedItemId)
			}

			configurationStoreId, err := configurationstores.ParseConfigurationStoreID(*configurationStoreIdRaw)
			if err != nil {
				return err
			}

			ok, err := metadata.Client.AppConfiguration.Exists(ctx, *configurationStoreId)
			if err != nil {
				return fmt.Errorf("while checking Configuration Store %q for feature %q existence: %v", *configurationStoreId, *nestedItemId, err)
			}
			if !ok {
				log.Printf("[DEBUG] Configuration Store %q for feature %q was not found - removing from state", *configurationStoreId, *nestedItemId)
				return metadata.MarkAsGone(nestedItemId)
			}

			client, err := metadata.Client.AppConfiguration.DataPlaneClientWithEndpoint(nestedItemId.ConfigurationStoreEndpoint)
			if err != nil {
				return err
			}

			kv, err := client.GetKeyValue(ctx, nestedItemId.Key, nestedItemId.Label, "", "", "", []string{})
			if err != nil {
				if v, ok := err.(autorest.DetailedError); ok {
					if utils.ResponseWasNotFound(autorest.Response{Response: v.Response}) {
						return metadata.MarkAsGone(nestedItemId)
					}
				} else {
					return fmt.Errorf("while checking for key %q existence: %+v", *nestedItemId, err)
				}
				return fmt.Errorf("while checking for key %q existence: %+v", *nestedItemId, err)
			}

			var fv FeatureValue
			err = json.Unmarshal([]byte(utils.NormalizeNilableString(kv.Value)), &fv)
			if err != nil {
				return fmt.Errorf("while unmarshalling underlying key's value: %+v", err)
			}

			model := FeatureResourceModel{
				ConfigurationStoreId: configurationStoreId.ID(),
				Description:          fv.Description,
				Enabled:              fv.Enabled,
				Key:                  strings.TrimPrefix(utils.NormalizeNilableString(kv.Key), fmt.Sprintf("%s/", FeatureKeyPrefix)),
				Name:                 fv.ID,
				Label:                utils.NormalizeNilableString(kv.Label),
				Tags:                 tags.Flatten(kv.Tags),
			}

			if kv.Locked != nil {
				model.Locked = *kv.Locked
			}

			if len(fv.Conditions.ClientFilters.Filters) > 0 {
				for _, f := range fv.Conditions.ClientFilters.Filters {
					switch f := f.(type) {
					case TimewindowFeatureFilter:
						twfp := f
						model.TimewindowFilters = append(model.TimewindowFilters, twfp.Parameters)
					case TargetingFeatureFilter:
						tfp := f
						model.TargetingFilters = append(model.TargetingFilters, tfp.Parameters.Audience)
					case PercentageFeatureFilter:
						pfp := f
						model.PercentageFilter = pfp.Parameters.Value
					default:
						return fmt.Errorf("while unmarshaling feature payload: unknown filter type %+v", f)
					}
				}
			}
			return metadata.Encode(&model)
		},
		Timeout: 5 * time.Minute,
	}
}

func (k FeatureResource) Update() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			nestedItemId, err := parse.ParseNestedItemID(metadata.ResourceData.Id())
			if err != nil {
				return fmt.Errorf("while parsing resource ID: %+v", err)
			}

			client, err := metadata.Client.AppConfiguration.DataPlaneClientWithEndpoint(nestedItemId.ConfigurationStoreEndpoint)
			if err != nil {
				return err
			}

			var model FeatureResourceModel
			if err := metadata.Decode(&model); err != nil {
				return fmt.Errorf("decoding %+v", err)
			}

			configurationStoreId, err := configurationstores.ParseConfigurationStoreID(model.ConfigurationStoreId)
			if err != nil {
				return err
			}

			metadata.Client.AppConfiguration.AddToCache(*configurationStoreId, nestedItemId.ConfigurationStoreEndpoint)

			if metadata.ResourceData.HasChange("tags") || metadata.ResourceData.HasChange("enabled") || metadata.ResourceData.HasChange("locked") || metadata.ResourceData.HasChange("description") {
				// Remove the lock, if any. We will put it back again if the model says so.
				if _, err = client.DeleteLock(ctx, nestedItemId.Key, nestedItemId.Label, "", ""); err != nil {
					return fmt.Errorf("while unlocking key/label pair %s/%s: %+v", nestedItemId.Key, nestedItemId.Label, err)
				}
				err = createOrUpdateFeature(ctx, client, model)
				if err != nil {
					return fmt.Errorf("while updating feature: %+v", err)
				}
			}

			return nil
		},
		Timeout: 30 * time.Minute,
	}
}

func (k FeatureResource) Delete() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			nestedItemId, err := parse.ParseNestedItemID(metadata.ResourceData.Id())
			if err != nil {
				return fmt.Errorf("while parsing resource ID: %+v", err)
			}

			client, err := metadata.Client.AppConfiguration.DataPlaneClientWithEndpoint(nestedItemId.ConfigurationStoreEndpoint)
			if err != nil {
				return err
			}

			kv, err := client.GetKeyValues(ctx, nestedItemId.Key, nestedItemId.Label, "", "", []string{})
			if err != nil {
				return fmt.Errorf("while checking for feature's %q existence: %+v", nestedItemId.Key, err)
			}
			keysFound := kv.Values()
			if len(keysFound) == 0 {
				return nil
			}

			if _, err = client.DeleteLock(ctx, nestedItemId.Key, nestedItemId.Label, "", ""); err != nil {
				return fmt.Errorf("while unlocking key %q: %+v", *nestedItemId, err)
			}

			if _, err = client.DeleteKeyValue(ctx, nestedItemId.Key, nestedItemId.Label, ""); err != nil {
				return fmt.Errorf("while removing key %q: %+v", *nestedItemId, err)
			}

			return nil
		},
		Timeout: 30 * time.Minute,
	}
}

func (k FeatureResource) IDValidationFunc() pluginsdk.SchemaValidateFunc {
	return validate.NestedItemId
}

func createOrUpdateFeature(ctx context.Context, client *appconfiguration.BaseClient, model FeatureResourceModel) error {
	rawKey := model.Name
	if model.Key != "" {
		rawKey = model.Key
	}
	featureKey := fmt.Sprintf("%s/%s", FeatureKeyPrefix, rawKey)

	entity := appconfiguration.KeyValue{
		Key:         utils.String(featureKey),
		Label:       utils.String(model.Label),
		Tags:        tags.Expand(model.Tags),
		ContentType: utils.String(FeatureKeyContentType),
		Locked:      utils.Bool(model.Locked),
	}

	value := FeatureValue{
		ID:          model.Name,
		Description: model.Description,
		Enabled:     model.Enabled,
	}

	value.Conditions.ClientFilters.Filters = make([]interface{}, 0)
	if model.PercentageFilter > 0 {
		value.Conditions.ClientFilters.Filters = append(value.Conditions.ClientFilters.Filters, PercentageFeatureFilter{
			Name:       PercentageFilterName,
			Parameters: PercentageFilterParameters{Value: model.PercentageFilter},
		})
	}

	if len(model.TargetingFilters) > 0 {
		for _, tgtf := range model.TargetingFilters {
			value.Conditions.ClientFilters.Filters = append(value.Conditions.ClientFilters.Filters, TargetingFeatureFilter{
				Name:       TargetingFilterName,
				Parameters: TargetingFilterParameters{Audience: tgtf},
			})
		}
	}

	if len(model.TimewindowFilters) > 0 {
		for _, twf := range model.TimewindowFilters {
			value.Conditions.ClientFilters.Filters = append(value.Conditions.ClientFilters.Filters, TimewindowFeatureFilter{
				Name:       TimewindowFilterName,
				Parameters: twf,
			})
		}
	}

	valueBytes, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("while marshalling FeatureValue struct: %+v", err)
	}
	entity.Value = utils.String(string(valueBytes))
	if _, err = client.PutKeyValue(ctx, featureKey, model.Label, &entity, "", ""); err != nil {
		return err
	}

	if model.Locked {
		if _, err = client.PutLock(ctx, featureKey, model.Label, "", ""); err != nil {
			return fmt.Errorf("while locking key/label pair %s/%s: %+v", model.Name, model.Label, err)
		}
	} else {
		if _, err = client.DeleteLock(ctx, featureKey, model.Label, "", ""); err != nil {
			return fmt.Errorf("while unlocking key/label pair %s/%s: %+v", model.Name, model.Label, err)
		}
	}

	return nil
}
func (k FeatureResource) StateUpgraders() sdk.StateUpgradeData {
	return sdk.StateUpgradeData{
		SchemaVersion: 1,
		Upgraders: map[int]pluginsdk.StateUpgrade{
			0: migration.FeatureResourceV0ToV1{},
		},
	}
}
